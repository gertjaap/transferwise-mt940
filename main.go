package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"time"
)

type Statement struct {
	Transactions []StatementTransaction `json:"transactions"`
}

type StatementTransaction struct {
	Type        string             `json:"type"`
	Date        time.Time          `json:"date"`
	Description string             `json:"description"`
	Amount      TransactionAmount  `json:"amount"`
	Fees        TransactionAmount  `json:"totalFees"`
	Details     TransactionDetails `json:"details"`
}

type TransactionDetails struct {
	Type         string            `json:"type"`
	Amount       TransactionAmount `json:"amount"`
	Description  string            `json:"description"`
	SourceAmount TransactionAmount `json:"sourceAmount"`
	TargetAmount TransactionAmount `json:"targetAmount"`
}

type TransactionAmount struct {
	Value    float64 `json:"value"`
	Currency string  `json:"currency"`
}

type DollarReserves struct {
	Value float64
	Rate  float64
}

func main() {

	totalTransactions := make([]StatementTransaction, 0)

	intervalTime := time.Now()
	for {
		if intervalTime.Before(time.Date(2018, time.April, 1, 0, 0, 0, 0, time.UTC)) {
			break
		}

		startTime, err := intervalTime.Add(time.Hour * -24 * 60).UTC().MarshalJSON()
		endTime, err := intervalTime.Add(time.Second * -1).UTC().MarshalJSON()
		// Lob off the quotes, last three digits
		startTime = startTime[1 : len(startTime)-5]
		endTime = endTime[1 : len(endTime)-5]

		bStatement, err := doTransferWiseApiCall(fmt.Sprintf("/v1/borderless-accounts/1540801/statement.json?currency=USD&intervalStart=%sZ&intervalEnd=%sZ", startTime, endTime))
		if err != nil {
			fmt.Printf("ERROR: %s\n", err.Error())
			return
		}
		//fmt.Printf("JSON:\n\n%s\n", string(bStatement))
		s := Statement{}
		jsonErr := json.Unmarshal(bStatement, &s)
		if jsonErr != nil {
			fmt.Printf("ERROR parsing json: %s\n====\n%s", jsonErr.Error(), string(bStatement))
			panic(jsonErr)
		}

		totalTransactions = append(totalTransactions, s.Transactions...)
		intervalTime = intervalTime.Add(time.Hour * -24 * 60)
	}

	for i := len(totalTransactions)/2 - 1; i >= 0; i-- {
		opp := len(totalTransactions) - 1 - i
		totalTransactions[i], totalTransactions[opp] = totalTransactions[opp], totalTransactions[i]
	}

	balance := float64(0)
	totalReserves := float64(0)
	reserves := make([]*DollarReserves, 0)
	f, err := os.Create("mt940.txt")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	f.Write([]byte(":940:\r\n:20:940A150326\r\n:25:Transferwise Account\r\n:28:00360/00\r\n:60F:C150325EUR000000000000.00\r\n"))

	for _, t := range totalTransactions {
		//fmt.Printf("%s - %s - %f\n", t.Type, t.Details.Type, t.Amount.Value)
		if t.Details.Type == "CONVERSION" && !(t.Details.SourceAmount.Currency == "EUR" || t.Details.TargetAmount.Currency == "EUR") {
			// ignore transactions that don't convert EUR into USD or vice versa. We want to know the EUR value of a spend, so ... yeah
			continue
		}

		balance += t.Amount.Value

		if t.Details.Type == "CONVERSION" {

			rate := float64(1)
			if t.Details.SourceAmount.Currency == "USD" {
				rate = t.Details.SourceAmount.Value / t.Details.TargetAmount.Value
			} else {
				rate = t.Details.TargetAmount.Value / t.Details.SourceAmount.Value
			}
			//fmt.Printf("Found conversion from %f %s to %f %s - Balance: %f - Total Reserves: %f\n", t.Details.SourceAmount.Value, t.Details.SourceAmount.Currency, t.Details.TargetAmount.Value, t.Details.TargetAmount.Currency, balance, totalReserves)
			if balance-totalReserves > 0 {
				reserves = append(reserves, &DollarReserves{Value: balance - totalReserves, Rate: rate})
				totalReserves += balance - totalReserves
			}
			continue
		}

		if t.Details.Type == "CARD" {
			// Okay so here we're actually spending stuff. Let's see.
			eurAmount := 0 - getEURValue(reserves, 0-t.Amount.Value)
			totalReserves -= 0 - t.Amount.Value
			if t.Date.After(time.Date(2019, time.February, 7, 0, 0, 0, 0, time.UTC)) {
				dOrC := "D"
				if t.Amount.Value > 0 {
					dOrC = "C"
				}
				f.Write([]byte(fmt.Sprintf(":61:%s%s%012.2fN5450300091494      US00TRAN000000000\r\n", t.Date.Format("060102"), dOrC, math.Abs(eurAmount))))
				f.Write([]byte(fmt.Sprintf(":86:%s (USD %0.2f)\r\n", t.Details.Description, t.Amount.Value)))
			}

		}

	}

	f.Write([]byte(":62F:C150326EUR000000000000.00\r\n"))

}

func printReserves(reserves []*DollarReserves) {
	for i, r := range reserves {
		if r.Value > 0 {
			fmt.Printf("Reserve %d [%f USD] Rate [%f]\n", i, r.Value, r.Rate)
		}
	}
}

func getEURValue(reserves []*DollarReserves, amountUSD float64) float64 {
	//printReserves(reserves)
	eurAmount := float64(0)
	remainingAmountUSD := float64(amountUSD)
	for _, r := range reserves {
		if r.Value > 0 {
			valueToProcess := math.Min(r.Value, remainingAmountUSD)
			remainingAmountUSD -= valueToProcess
			r.Value -= valueToProcess
			eurAmount += (valueToProcess / r.Rate)
		}
		if remainingAmountUSD == 0 {
			return eurAmount
		}
	}
	if remainingAmountUSD > 0 {
		panic("Can't convert amount, no dollars left... huh?")
	}
	return 0
}

func doTransferWiseApiCall(url string) ([]byte, error) {
	c := http.Client{
		Timeout: time.Second * 2, // Maximum of 2 secs
	}

	url = "https://api.transferwise.com" + url
	//fmt.Printf("Loading from %s\n", url)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+os.Getenv("TWAPI_TOKEN"))

	res, getErr := c.Do(req)
	if getErr != nil {
		return nil, getErr
	}

	defer res.Body.Close()

	body, readErr := ioutil.ReadAll(res.Body)
	if readErr != nil {
		return nil, readErr
	}
	return body, nil
}
