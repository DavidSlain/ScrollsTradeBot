package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var Prices = make(map[string]int)

var Gold int

var TradeRoom Channel

type TradeStatus struct {
	Partner Player
	Updated bool
	Their   struct {
		Value    int
		Cards    map[string]int
		Gold     int
		Accepted bool
	}
	My struct {
		Value    int
		Cards    map[string]int
		Gold     int
		Accepted bool
	}
}

func LoadPrices() {
	resp, err := http.Get("http://www.scrollsguide.com/trade")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	var b bytes.Buffer
	_, err = io.Copy(&b, resp.Body)

	s := string(b.Bytes())
	re := regexp.MustCompile("<td class='row1 ex'>([A-Z][A-Za-z ]+)+</td><td class='row1'>([0-9]+)g</td><td class='row1'>([0-9]+)g</td>")
	found := re.FindAllStringSubmatch(s, -1)

	for _, matches := range found {
		card := matches[1]
		buy, _ := strconv.Atoi(matches[2])
		sell, _ := strconv.Atoi(matches[3])

		lowerPrice, upperPrice := 0, 0

		switch CardRarities[card] {
		case 0:
			lowerPrice = 75
			upperPrice = 150
		case 1:
			lowerPrice = 300
			upperPrice = 500
		case 2:
			lowerPrice = 600
			upperPrice = 1500
		}

		if buy < lowerPrice {
			buy = lowerPrice
		}
		if sell < lowerPrice {
			sell = lowerPrice
		}
		if buy > upperPrice {
			buy = upperPrice
		}
		if sell > upperPrice {
			sell = upperPrice
		}
		price := (buy + sell) / 2

		Prices[card] = price
	}
}

func MinimumValue(card string) int {
	switch CardRarities[card] {
	case 0:
		return 25
	case 1:
		return 50
	case 2:
		return 100
	}
	return -1
}

func (s *State) DeterminePrice(card string, num int, buy bool) int {
	price := 0
	stocked := Stocks[Bot][card]

	for i := 0; i < num; i++ {
		if buy {
			price += int(math.Max(float64(MinimumValue(card)), float64(Prices[card])*math.Exp(-float64(stocked*stocked)/20)))
			stocked++

		} else {
			if stocked > 1 {
				stocked--
			}
			price += int(math.Max(float64(MinimumValue(card)), float64(Prices[card])*math.Exp(-float64(stocked*stocked)/20)*1.15))
		}
	}
	return price
}

func (s *State) ParseTradeResponse(v MTradeResponse) {
	if v.Status == "DECLINE" {
		s.chTradeResponse <- false
	} else {
		s.chTradeResponse <- true
	}
}

func (s *State) ParseTradeView(v MTradeView) {
	my := v.From
	their := v.To
	tradePartner := Player(v.To.Profile.Name)
	if their.Profile.Id == PlayerIds[Bot] {
		my, their = their, my
		tradePartner = Player(v.From.Profile.Name)
	}

	convertAndCount := func(cardIds []int, player Player) map[string]int {
		count := make(map[string]int)
		for _, id := range cardIds {
			for _, card := range Libraries[player].Cards {
				if card.Id == id {
					cardName := CardTypes[CardId(card.TypeId)]
					count[cardName] = count[cardName] + 1
					break
				}
			}
		}
		return count
	}

	ts := TradeStatus{}
	ts.Updated = v.Modified
	ts.Partner = tradePartner
	ts.Their.Accepted = their.Accepted
	ts.Their.Cards = convertAndCount(their.CardIds, tradePartner)
	ts.Their.Gold = their.Gold
	ts.My.Accepted = my.Accepted
	ts.My.Cards = convertAndCount(my.CardIds, Bot)
	ts.My.Gold = my.Gold

	s.chTradeStatus <- ts
}

func (s *State) InitiateTrade(player Player, timeout time.Duration) chan TradeStatus {
	s.SendRequest(Request{"msg": "TradeInvite", "profile": PlayerIds[player]})
	accepted := false
	TradeRoom = ""

	cancel := time.After(timeout)
	for {
		if TradeRoom != "" && accepted {
			break
		}

		select {
		case ok := <-s.chTradeResponse:
			if !ok { // they rejected the trade invite
				log.Printf("REJECT")
				return nil
			} else {
				accepted = true
			}
		case m := <-s.Listen(): // find out what room we're trading in
			if strings.HasPrefix(string(m.Channel), "trade-") {
				log.Printf("ACCEPT")
				TradeRoom = m.Channel
			}
		case <-cancel:
			// TODO: what happens if the player accepts after timeout?
			return nil
		}
	}
	return s.chTradeStatus
}

func (s *State) Trade(tradePartner Player) (ts TradeStatus) {
	// Send them a trade invite and see if they accept
	chTradeStatus := s.InitiateTrade(tradePartner, 30*time.Second)
	if chTradeStatus != nil {
		defer s.LeaveRoom(TradeRoom)
		lastActivity := time.Now()
		startTime := time.Now()

		minuteWarning := false
		tenSecondWarning := false
		lastIdleWarning := time.Now()

		cardsChanged := false

		s.Say(TradeRoom, fmt.Sprintf("Welcome %s. This is an automated trading unit. If you don't know what to do, just say '!help'.", tradePartner))

		request := WTBrequests[tradePartner]
		if len(request) > 0 {
			cardIds := make([]int, 0)

			for cardName, num := range request {
				for _, card := range Libraries[Bot].Cards {
					if card.Tradable && CardTypes[CardId(card.TypeId)] == cardName {
						cardIds = append(cardIds, card.Id)
						num--
						if num <= 0 {
							break
						}
					}
				}
			}
			s.SendRequest(Request{"msg": "TradeAddCards", "cardIds": cardIds})
			s.Say(TradeRoom, "I've initialized the trade room with your last WTB request.")
		}

		messages := s.Listen()
		ticker := time.Tick(time.Second)
		for {
			select {
			case m := <-messages:
				if m.From == tradePartner && m.Channel == TradeRoom {
					lastActivity = time.Now()

					if m.Text == "!help" {
						s.Say(m.Channel, "Just add the scrolls you want to sell on your side. To buy scrolls from me, say '!add [scroll name]' "+
							"or '!remove [scroll name]' and I will update my side. Not sure about the gold? Just ask for the !price and I'll list it up.")

					} else if m.Text == "!price" {
						format := func(card string, num int) string {
							if num > 1 {
								return fmt.Sprintf("%dx %s", num, card)
							}
							return card
						}

						theirValue := make(map[string]int)
						for card, num := range ts.Their.Cards {
							theirValue[format(card, num)] = s.DeterminePrice(card, num, true)
						}
						myValue := make(map[string]int)
						for card, num := range ts.My.Cards {
							myValue[format(card, num)] = s.DeterminePrice(card, num, false)
						}

						list := func(value map[string]int) []string {
							lines := make([]string, len(value))
							for i, _ := range lines {
								mostGold := 0
								nextCard := ""

								for card, gold := range value {
									if gold > mostGold {
										mostGold = gold
										nextCard = card
									}
								}
								lines[i] = fmt.Sprintf("%s for %dg", nextCard, mostGold)
								value[nextCard] = 0
							}
							return lines
						}
						msg := ""
						if len(theirValue) > 0 {
							msg += fmt.Sprintf("I'll buy %s. ", strings.Join(list(theirValue), ", "))
						}
						if len(myValue) > 0 {
							msg += fmt.Sprintf("I'll sell %s. ", strings.Join(list(myValue), ", "))
						}
						diff := ts.Their.Value - ts.My.Value
						if diff < 0 {
							msg += fmt.Sprintf("Thus you owe me %dg.", -diff)
						} else {
							msg += fmt.Sprintf("Thus I owe you %dg.", diff)
						}
						s.Say(m.Channel, msg)

					} else if strings.HasPrefix(m.Text, "!add") {
						cardName := matchCardName(strings.Replace(m.Text, "!add ", "", 1))
						stocked, ok := Stocks[Bot][cardName]

						alreadyOffered := ts.My.Cards[cardName]

						if !ok {
							s.Say(m.Channel, fmt.Sprintf("There is no scroll named '%s'.", cardName))
						} else if alreadyOffered == stocked {
							s.Say(m.Channel, fmt.Sprintf("Sorry, %s is out of stock.", cardName))
						} else {
							for _, card := range Libraries[Bot].Cards {
								if card.Tradable && CardTypes[CardId(card.TypeId)] == cardName {
									if alreadyOffered == 0 {
										s.SendRequest(Request{"msg": "TradeAddCards", "cardIds": []int{card.Id}})
										break
									}
									alreadyOffered--
								}
							}
						}

					} else if strings.HasPrefix(m.Text, "!remove") {
						cardName := matchCardName(strings.Replace(m.Text, "!remove ", "", 1))
						_, ok := Stocks[Bot][cardName]

						alreadyOffered := ts.My.Cards[cardName]

						if !ok {
							s.Say(m.Channel, fmt.Sprintf("There is no scroll named '%s'.", cardName))
						} else if alreadyOffered == 0 {
							s.Say(m.Channel, fmt.Sprintf("%s is not part of this trade!", cardName))
						} else {
							for _, card := range Libraries[Bot].Cards {
								if card.Tradable && CardTypes[CardId(card.TypeId)] == cardName {
									if alreadyOffered == 1 {
										s.SendRequest(Request{"msg": "TradeRemoveCard", "cardId": card.Id})
										break
									}
									alreadyOffered--
								}
							}
						}
					}
				}

				if m.From == "Scrolls" && m.Channel == TradeRoom && strings.HasPrefix(m.Text, "Trade ended") {
					return
				}

			case newTradeStatus := <-chTradeStatus:
				oldValueSum := ts.Their.Value + ts.My.Value

				ts = newTradeStatus
				// sanity check..
				if ts.Partner != tradePartner {
					s.Whisper("redefiance", fmt.Sprintf("I failed so hard >.> %s != %s", ts.Partner, tradePartner))
					return
				}

				if ts.Updated {
					lastActivity = time.Now()
				}

				if ts.My.Accepted && ts.Their.Accepted {
					s.Say(TradeRoom, "Thanks!")
					Gold += ts.Their.Gold
					Gold -= ts.My.Gold
					for card, num := range ts.Their.Cards {
						Stocks[Bot][card] += num
					}
					for card, num := range ts.My.Cards {
						Stocks[Bot][card] -= num
					}

					alreadySold := make(map[string]bool)
					cardIds := make([]int, 0)

					for _, card := range Libraries[Bot].Cards {
						cardName := CardTypes[CardId(card.TypeId)]
						if !alreadySold[cardName] && card.Tradable && s.DeterminePrice(cardName, 1, false) <= MinimumValue(cardName) {
							alreadySold[cardName] = true
							cardIds = append(cardIds, card.Id)
						}
					}
					if len(cardIds) > 0 {
						s.SendRequest(Request{"msg": "SellCards", "cardIds": cardIds})
						s.SendRequest(Request{"msg": "ProfileDataInfo"})
						s.SendRequest(Request{"msg": "LibraryView"})
					}
					logTrade(ts)
					return
				}

				for card, num := range ts.Their.Cards {
					ts.Their.Value += s.DeterminePrice(card, num, true)
				}

				for card, num := range ts.My.Cards {
					ts.My.Value += s.DeterminePrice(card, num, false)
				}

				if oldValueSum != ts.Their.Value+ts.My.Value {
					cardsChanged = true
				}

				goldNeeded := ts.Their.Value - ts.My.Value + ts.Their.Gold
				if goldNeeded != ts.My.Gold {
					if goldNeeded > 0 && Gold >= goldNeeded {
						s.SendRequest(Request{"msg": "TradeSetGold", "gold": goldNeeded})
					} else if ts.My.Gold != 0 {
						s.SendRequest(Request{"msg": "TradeSetGold", "gold": 0})
					}
				}

			case <-ticker:
				if time.Now().After(lastActivity.Add(time.Minute)) && time.Now().After(lastIdleWarning.Add(time.Minute)) {
					s.Say(TradeRoom, "You have been idle for a minute. This trade window will close in 30 seconds unless you interact with it.")
					lastIdleWarning = time.Now()
				}

				if time.Now().After(lastActivity.Add(time.Minute + 30*time.Second)) {
					s.Say(TradeRoom, "Time's up!")
					return
				}

				if !minuteWarning && time.Now().After(startTime.Add(4*time.Minute)) {
					s.Say(TradeRoom, "Please finish the trade within the next minute.")
					minuteWarning = true
				}
				if !tenSecondWarning && time.Now().After(startTime.Add(4*time.Minute+50*time.Second)) {
					s.Say(TradeRoom, "You have 10 seconds left to finish the trade.")
					tenSecondWarning = true
				}
				if time.Now().After(startTime.Add(time.Minute * 5)) {
					s.Say(TradeRoom, "Time's up!")
					return
				}

				if cardsChanged && time.Now().After(lastActivity.Add(time.Second*2)) {
					cardsChanged = false

					value := ts.Their.Value - ts.My.Value
					if value > Gold {
						s.Say(TradeRoom, fmt.Sprintf("Sorry - I only have %d gold at my disposal. Please take something out.", Gold))
					} else if value < 0 {
						s.Say(TradeRoom, fmt.Sprintf("Please set your gold offer to %dg", -value))
					}
				}

				if tradePartner == "redefiance" {
					if time.Now().After(lastActivity.Add(time.Second * 7)) {
						s.SendRequest(Request{"msg": "TradeAcceptBargain"})

					}
				} else {
					if ts.Their.Value+ts.Their.Gold == ts.My.Value+ts.My.Gold &&
						!ts.My.Accepted && ts.Their.Value+ts.Their.Gold > 0 &&
						time.Now().After(lastActivity.Add(time.Second*7)) {
						s.SendRequest(Request{"msg": "TradeAcceptBargain"})
					}
				}
			}
		}
	}
	return
}