package ui

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
)

//Response 回复结构
type Response struct {
	Result   bool        `json:"result"`
	ErrorMsg string      `json:"errorMsg"`
	Output   string      `json:"output"`
	Content  interface{} `json:"content"`
}

//PUT restful请求PUT操作
const (
	PUT    = "PUT"
	POST   = "POST"
	GET    = "GET"
	DELETE = "DELETE"
)

const (
	applicationTypeJSON = "application/json"
	applicationTypeXML  = "application/xml"
)

const httpHeaderContentType string = "Content-Type"

const httpHeaderAccept string = "Accept"

//Request 请求结构
type Request struct {
	URL     string      `json:"url"`
	Type    string      `json:"type"`
	Content interface{} `json:"content"`
}

//SendRequestByJSON 用于发送json格式的http请求
func (reqInfo *Request) SendRequestByJSON() ([]byte, error) {
	jsonTypeContent, _ := json.Marshal(reqInfo.Content)
	body := strings.NewReader(string(jsonTypeContent))

	client := &http.Client{}

	req, _ := http.NewRequest(reqInfo.Type, reqInfo.URL, body)
	req.Header.Set(httpHeaderContentType, applicationTypeJSON)
	req.Header.Set(httpHeaderAccept, applicationTypeJSON)

	resp, err := client.Do(req) //发送
	if err != nil {
		return []byte{}, err
	}
	defer resp.Body.Close() //一定要关闭resp.Body
	data, _ := ioutil.ReadAll(resp.Body)

	respBody := data

	return respBody, err
}
