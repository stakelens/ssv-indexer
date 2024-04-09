package main

import "encoding/json"

type WebSocketResponse struct {
	Type   string          `json:"type"`
	Filter Filter          `json:"filter"`
	Data   json.RawMessage `json:"data"`
}

type Filter struct {
	From      int    `json:"from"`
	To        int    `json:"to"`
	Role      string `json:"role"`
	PublicKey string `json:"publicKey"`
}

type Data struct {
	Signature string   `json:"Signature"`
	Signers   []int    `json:"Signers"`
	Message   Message  `json:"Message"`
	FullData  FullData `json:"FullData"`
}

type Message struct {
	MsgType                  int      `json:"MsgType"`
	Height                   int      `json:"Height"`
	Round                    int      `json:"Round"`
	Identifier               string   `json:"Identifier"`
	Root                     []int    `json:"Root"`
	DataRound                int      `json:"DataRound"`
	RoundChangeJustification []string `json:"RoundChangeJustification"`
	PrepareJustification     []string `json:"PrepareJustification"`
}

type FullData struct {
	Duty                       Duty     `json:"Duty"`
	Version                    string   `json:"Version"`
	PreConsensusJustifications []string `json:"PreConsensusJustifications"`
	DataSSZ                    string   `json:"DataSSZ"`
}

type Duty struct {
	Type                          int    `json:"Type"`
	PubKey                        string `json:"PubKey"`
	Slot                          string `json:"Slot"`
	ValidatorIndex                string `json:"ValidatorIndex"`
	CommitteeIndex                int    `json:"CommitteeIndex"`
	CommitteeLength               int    `json:"CommitteeLength"`
	CommitteesAtSlot              int    `json:"CommitteesAtSlot"`
	ValidatorCommitteeIndex       int    `json:"ValidatorCommitteeIndex"`
	ValidatorSyncCommitteeIndices []int  `json:"ValidatorSyncCommitteeIndices"`
}
