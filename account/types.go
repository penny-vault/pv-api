// Copyright 2021-2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package account

import "time"

type TaxDisposition string

const (
	LongTermCapitalGain  TaxDisposition = "LTC"
	ShortTermCapitalGain TaxDisposition = "STC"
	TaxDeferred          TaxDisposition = "DEFERRED"
	TaxFree              TaxDisposition = "ROTH"
	IncomeTax            TaxDisposition = "INCOME"
)

type Account struct {
	ID          int64  `json:"id"`
	UserID      string `json:"user_id"`
	ReferenceID string `json:"reference_id"`
	Name        string `json:"name"`
	AccessToken string `json:"access_token"`
	ItemID      string `json:"item_id"`
	Cursor      string `json:"cursor"`
}

type Category struct {
	Name      string  `json:"category"`
	Primary   string  `json:"primary"`
	Secondary string  `json:"secondary"`
	Amount    float64 `json:"amount"`
}

type Location struct {
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Address     string  `json:"address"`
	City        string  `json:"city"`
	Country     string  `json:"country"`
	Region      string  `json:"region"`
	PostalCode  string  `json:"postal_code"`
	StoreNumber string  `json:"store_number"`
}

type Transaction struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	AccountID   int64      `json:"account_id"`
	Source      string     `json:"source"`
	SourceID    string     `json:"source_id"`
	SequenceNum int64      `json:"sequence_num"`
	TxDate      time.Time  `json:"tx_date"`
	Payee       string     `json:"payee"`
	Location    Location   `json:"locations"`
	Icon        string     `json:"icon"`
	Category    []Category `json:"category"`
	Tags        []string   `json:"tags"`
	Reviewed    bool       `json:"reviewed"`
	Cleared     bool       `json:"cleared"`
	Amount      float64    `json:"amount"`
	Balance     float64    `json:"balance"`
	Memo        string     `json:"memo"`
	Attachments []string   `json:"attachments"`
	Related     []string   `json:"related"`

	Commission    float64                `json:"commission"`
	CompositeFIGI string                 `json:"composite_figi"`
	NumShares     float64                `json:"num_shares"`
	PricePerShare float64                `json:"price_per_share"`
	Ticker        string                 `json:"ticker"`
	Justification map[string]interface{} `json:"justification"`

	TaxTreatment TaxDisposition `json:"tax_treatment"`
	GainLoss     float64        `json:"gain_loss"`

	Created     time.Time `json:"created"`
	LastChanged time.Time `json:"lastchanged"`
}
