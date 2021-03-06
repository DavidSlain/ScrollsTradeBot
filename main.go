// TODO: !add [scroll] [quantity]
// TODO: reduce price for multiple scrolls
// TODO: ask for scrolls prices outside trade

package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reNumbers = regexp.MustCompile(`x?(\d+)x?`)
var reInvalidChars = regexp.MustCompile("[^a-z'0-9 ]")

var WTBrequests = make(map[Player]map[string]int)
var Bot Player

func main() {
	f, err := os.OpenFile("system.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	log.SetOutput(f)
	// log.SetOutput(ioutil.Discard)

	// startBot("I live again!")
	startBot("")
}

func startBot(helloMessage string) {
	login, err := ioutil.ReadFile("login.txt")
	if err != nil {
		panic(err)
	}
	split := strings.Split(string(login), "\n")
	if len(split) != 2 {
		panic("could not read email/password from login.txt")
	}

	s, chAlive := Connect(split[0], split[1])
	s.JoinRoom("clockwork")
	if helloMessage != "" {
		s.Say("clockwork", helloMessage)
	}

	upSince := time.Now()

	chKillThread := make(chan bool, 1)

	go func() {
		queue := make([]Player, 0)

		chReadyToTrade := make(chan bool, 100)
		currentlyTrading := false

		messages := s.Listen()
		defer s.Shut(messages)

		for {
			select {
			case <-chKillThread:
				return

			case <-chReadyToTrade:
				if len(queue) == 0 {
					s.Say("clockwork", "Finished trading.")
					currentlyTrading = false
				} else {
					currentlyTrading = true

					go func() {
						waiting := make([]string, len(queue)-1)
						for i, name := range queue[1:] {
							waiting[i] = string(name)
						}
						if len(waiting) > 0 {
							s.Say("clockwork", fmt.Sprintf("Now trading with [%s] < %s", queue[0], strings.Join(waiting, " < ")))
						} else {
							s.Say("clockwork", fmt.Sprintf("Now trading with [%s].", queue[0]))
						}

						stockBefore := Stocks[Bot]
						if stockBefore == nil {
							stockBefore = make(map[string]int)
						}

						ts := s.Trade(queue[0])

						aquired := make([]string, 0)
						lost := make([]string, 0)
						for card, num := range ts.Their.Cards {
							if stockBefore[card] == 0 {
								aquired = append(aquired, card)
							}
							stockBefore[card] = stockBefore[card] + num
						}
						for card, num := range ts.My.Cards {
							if stockBefore[card] <= num {
								lost = append(lost, card)
							}
							stockBefore[card] = stockBefore[card] - num
						}
						if len(aquired) > 0 {
							s.Say("clockwork", fmt.Sprintf("I've just aquired %s.", strings.Join(aquired, ", ")))
						}
						if len(lost) > 0 {
							s.Say("clockwork", fmt.Sprintf("I've just sold my last %s.", strings.Join(lost, ", ")))
						}

						queue = queue[1:]
						chReadyToTrade <- true
					}()
				}

			case m := <-messages:
				if m.From == "redefiance" && strings.HasPrefix(m.Text, "!say ") {
					s.Say("clockwork", strings.TrimPrefix(m.Text, "!say "))
				}

				forceWhisper := false
				replyMsg := ""
				command := strings.ToLower(m.Text)

				// if m.From != "redefiance" {
				if m.From == "Great_Marcoosai" {
					command = "" // banned!

				}

				if strings.HasPrefix(command, "wt") {
					command = strings.Replace(command, "wt", "!wt", 1)
				}

				if m.Channel == "WHISPER" && !strings.HasPrefix(command, "!") {
					command = "!" + command
				}

				if command == "!wts" || command == "!wtb" {
					replyMsg = "You need to add a list of cards to this command, seperated by commata. Multipliers like '2x' are allowed."
					forceWhisper = true
				}

				if strings.HasPrefix(command, "!wts ") && m.Channel != TradeRoom {
					cards, failedWords := parseCardList(strings.TrimPrefix(command, "!wts "))
					if len(cards) > 0 {
						words := make([]string, 0, len(cards))
						goldSum := 0
						for card, num := range cards {
							gold := s.DeterminePrice(card, num, true)
							numStr := ""
							if num != 1 {
								numStr = fmt.Sprintf("%dx ", num)
							}
							words = append(words, fmt.Sprintf("%s%s %d", numStr, card, gold))
							goldSum += gold
						}

						s1, s2, s3, s4 := "will", "", "", ""
						if goldSum > GoldForTrade() {
							s1 = "would"
							s3 = fmt.Sprintf(" I currently only have %dg.", GoldForTrade())
						}
						if len(words) > 1 {
							s2 = fmt.Sprintf(" That sums up to %dg.", goldSum)
						}
						if len(failedWords) > 0 {
							s4 = fmt.Sprintf(" I don't know what '%s' is.", strings.Join(failedWords, ", "))
						}

						replyMsg = fmt.Sprintf("I %s pay %s.%s%s%s", s1, strings.Join(words, ", "), s2, s3, s4)
						forceWhisper = true
					}
				}

				if strings.HasPrefix(command, "!wtb ") && m.Channel != TradeRoom {
					cards, failedWords := parseCardList(strings.TrimPrefix(command, "!wtb "))
					WTBrequests[m.From] = cards
					if len(cards) > 0 {
						words := make([]string, 0, len(cards))
						numItems := 0
						goldSum := 0
						hasAll := true
						for card, num := range cards {
							forceNumStr := false
							numItems += num
							if stocked := Stocks[Bot][card]; num > stocked {
								num = stocked
								hasAll = false
								forceNumStr = true
								if num == 0 {
									continue
								}
							}

							gold := s.DeterminePrice(card, num, false)
							numStr := ""
							if forceNumStr || num != 1 {
								numStr = fmt.Sprintf("%dx ", num)
							}
							words = append(words, fmt.Sprintf("%s%s %d", numStr, card, gold))
							goldSum += gold
						}

						s1, s2, s3 := "", "", ""
						if !hasAll {
							s1 = " That's all I have."
						}
						if len(words) > 1 {
							s2 = fmt.Sprintf(" That sums up to %dg.", goldSum)
						}
						if len(failedWords) > 0 {
							s3 = fmt.Sprintf(" I don't know what '%s' is.", strings.Join(failedWords, ", "))
						}

						if goldSum == 0 {
							if numItems == 1 {
								replyMsg = "I don't have "
								for card, _ := range cards {
									replyMsg += card
									break
								}
								replyMsg += " stocked."
							} else {
								replyMsg = "I don't have anything on that list stocked."
							}
							replyMsg += s3
						} else {
							replyMsg = fmt.Sprintf("I want to have %s.%s%s%s", strings.Join(words, ", "), s1, s2, s3)
						}
						forceWhisper = true
					}
				}

				if strings.HasPrefix(command, "!price ") || strings.HasPrefix(command, "!stock ") {
					cardName := matchCardName(strings.TrimPrefix(strings.TrimPrefix(command, "!stock "), "!price "))
					stocked, ok := Stocks[Bot][cardName]
					if !ok {
						replyMsg = "There is no card named '" + cardName + "'"
					} else {
						if stocked == 0 {
							price := s.DeterminePrice(cardName, 1, true)
							replyMsg = cardName + " is out of stock. "
							if price > GoldForTrade() {
								replyMsg += fmt.Sprintf("I would buy for %dg, but I don't have that much (base value %dg).", price, BaseValue(cardName))
							} else {
								replyMsg += fmt.Sprintf("I'm buying for %dg (base value %dg).", price, BaseValue(cardName))
							}

						} else {
							replyMsg = fmt.Sprintf("I'm buying %s for %dg and selling for %dg (base value %dg, %d stocked).", cardName,
								s.DeterminePrice(cardName, 1, true), s.DeterminePrice(cardName, 1, false), BaseValue(cardName), stocked)
						}
					}

					if rand.Float64() > 0.95 {
						replyMsg += " By the way, you can whisper me with 'wtb/wts [list of cards]' to easily check prices and availability for all cards you're interested in."
					}
				}

				if command == "!missing" {
					list := make([]string, 0)
					for _, card := range CardTypes {
						if Stocks[Bot][card] == 0 {
							list = append(list, card)
						}
					}
					replyMsg = fmt.Sprintf("I currently don't have %s. I'm paying extra for that!", strings.Join(list, ", "))
					forceWhisper = true
				}

				if command == "!stock" {
					commons := 0
					uncommons := 0
					rares := 0
					uniques := make(map[string]bool)
					totalValue := 0

					for _, card := range Libraries[Bot].Cards {
						name := CardTypes[CardId(card.TypeId)]
						if uniques[name] == false {
							totalValue += s.DeterminePrice(name, Stocks[Bot][name], false)
						}
						uniques[name] = true
						switch CardRarities[name] {
						case 0:
							commons++
						case 1:
							uncommons++
						case 2:
							rares++
						}
					}

					totalValue += Gold

					replyMsg = fmt.Sprintf("I have %d commons, %d uncommons and %d rares. That's %d%% of all card types, as well as %d gold. Total value is %dk gold.",
						commons, uncommons, rares, 100*len(uniques)/len(CardTypes), GoldForTrade(), int(totalValue/1000))
				}

				if command == "!help" && m.Channel != TradeRoom {
					replyMsg = "You can whisper me WTS or WTB requests. If you're interested in trading, you can queue up with '!trade'. You can also check the '!stock'"
				}

				if command == "!uptime" {
					replyMsg = fmt.Sprintf("Up since %s", time.Since(upSince))
				}

				if command == "!trade" || command == "!queue" {
					// replyMsg = "I'm currently under reconstruction, please wait a few minutes and try again."

					for i, player := range queue {
						if player == m.From {
							replyMsg = fmt.Sprintf("You are already queued for trading. Your position in the queue is %d.", i)
							break
						}
					}
					if replyMsg == "" {
						queue = append(queue, m.From)
						if len(queue) == 1 && !currentlyTrading {
							chReadyToTrade <- true
						} else {
							if m.Channel != "WHISPER" {
								replyMsg = fmt.Sprintf("%s: ", m.From)
							}
							replyMsg += fmt.Sprintf("You are now queued for trading. Your position in the queue is %d.", len(queue)-1)
						}
					}
				}

				if replyMsg != "" {
					if m.Channel == "WHISPER" {
						s.Whisper(m.From, replyMsg)
					} else {
						if forceWhisper {
							s.Whisper(m.From, replyMsg)
							s.Whisper(m.From, "To avoid spamming the channel, please use this command only in whisper. "+
								"By the way, you can use any other command in whisper as well!")
						} else {
							s.Say(m.Channel, replyMsg)
						}
					}
				}
			}
		}
	}()

	defer func() {
		if r := recover(); r != nil {
		}

		chKillThread <- true
		log.Print("Restarting in 5..")
		time.Sleep(time.Second * 5)
		startBot("I live again!")
	}()

	for {
		timeout := time.After(time.Minute * 1)
		ticker := time.Tick(30 * time.Second)
	InnerLoop:
		for {
			select {
			case <-chAlive:
				break InnerLoop
			case <-s.chQuit:
				log.Println("!!!QUIT!!!")
				return
			case <-timeout:
				log.Println("!!!TIMEOUT!!!")
				return
			case <-ticker:
				log.Println("..tick..")
			}
		}
	}
}

func matchCardName(input string) string {
	if input == "" {
		return input
	}

	minDist := 2
	bestFit := input

	for _, cardName := range CardTypes {
		dist := Levenshtein(strings.ToLower(input), strings.ToLower(cardName))
		if dist <= minDist {
			minDist = dist
			bestFit = cardName
		}
	}

	if minDist > 0 {
		for _, cardName := range CardTypes {
			for _, substr := range strings.Split(cardName, " ") {
				if substr == input {
					return cardName
				}
			}
		}

		for _, cardName := range CardTypes {
			if strings.Contains(strings.ToLower(cardName), strings.ToLower(input)) {
				return cardName
			}
		}
	}
	return bestFit
}

func parseCardList(str string) (cards map[string]int, failedWords []string) {
	cards = make(map[string]int)
	failedWords = make([]string, 0)

	for _, word := range strings.Split(str, ",") {
		word = reInvalidChars.ReplaceAllString(strings.ToLower(word), "")

		num := 1
		match := reNumbers.FindStringSubmatch(word)
		if len(match) == 2 {
			num, _ = strconv.Atoi(match[1])
			word = reNumbers.ReplaceAllString(word, "")
		}
		card := matchCardName(strings.Trim(word, " "))
		_, ok := CardRarities[card]
		if ok {
			cards[card] = num
		} else {
			failedWords = append(failedWords, word)
		}
	}
	return
}

func logTrade(ts TradeStatus) {
	file, err := os.OpenFile("trade.log", os.O_WRONLY+os.O_APPEND, 0)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	list := func(count map[string]int) string {
		s := make([]string, 0)
		for card, num := range count {
			if num > 1 {
				s = append(s, fmt.Sprintf("%dx %s", num, card))
			} else {
				s = append(s, card)
			}
		}
		return strings.Join(s, ",")
	}

	io.WriteString(file, fmt.Sprintf("%s: Traded with %s.\nTheir offer: [%dg] %s\nMy offer: [%dg] %s\n\n",
		time.Now().String(), ts.Partner, ts.Their.Gold, list(ts.Their.Cards), ts.My.Gold, list(ts.My.Cards)))
}
