package xpapimodel

import "encoding/json"

type APIResponseDatarefs struct {
	Data []DatarefInfo `json:"data"`
}

type DatarefInfo struct {
	ID         int    `json:"id"`
	IsWritable bool   `json:"is_writable"`
	Name       string `json:"name"`
	ValueType  string `json:"value_type"`
}

// Placeholder for WebSocket request structure (only used for confirmation)
type DatarefSubscriptionRequest struct {
	RequestID int64         `json:"req_id"`
	Type      string        `json:"type"`
	Params    ParamDatarefs `json:"params"`
}

type ParamDatarefs struct {
	Datarefs []SubDataref `json:"datarefs"`
}

type SubDataref struct {
	Id int `json:"id"`
}

type SubscriptionResponse struct {
	RequestID int64           `json:"req_id"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data,omitempty"`
	Success   bool            `json:"success,omitempty"`
}

// ErrorPayload is used if Type is "error".
type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
