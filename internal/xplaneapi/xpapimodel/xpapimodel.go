package xpapimodel

type APIResponseDatarefs struct {
	Data []DatarefInfo `json:"data"`
}

type APIResponseDatarefValue struct {
	Data any `json:"data"`
}

type DatarefInfo struct {
	ID         int    `json:"id"`
	IsWritable bool   `json:"is_writable"`
	Name       string `json:"name"`
	ValueType  string `json:"value_type"`
}

type Dataref struct {
	Name            string
	APIInfo         DatarefInfo
	Value           any
	DecodedDataType string
}

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
	RequestID int64                  `json:"req_id"`
	Type      string                 `json:"type"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Success   bool                   `json:"success,omitempty"`
}

// ErrorPayload is used if Type is "error".
type ErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
