package wechatoa

import "encoding/xml"

type wechatEnvelope struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	MsgID        string   `xml:"MsgId"`
	Content      string   `xml:"Content"`
	MediaID      string   `xml:"MediaId"`
	PicURL       string   `xml:"PicUrl"`
	Format       string   `xml:"Format"`
	ThumbMediaID string   `xml:"ThumbMediaId"`
	LocationX    string   `xml:"Location_X"`
	LocationY    string   `xml:"Location_Y"`
	Scale        string   `xml:"Scale"`
	Label        string   `xml:"Label"`
	Title        string   `xml:"Title"`
	Description  string   `xml:"Description"`
	URL          string   `xml:"Url"`
	Event        string   `xml:"Event"`
	EventKey     string   `xml:"EventKey"`
	Ticket       string   `xml:"Ticket"`
	Latitude     string   `xml:"Latitude"`
	Longitude    string   `xml:"Longitude"`
	Precision    string   `xml:"Precision"`
	Encrypt      string   `xml:"Encrypt"`
}

type encryptedEnvelope struct {
	XMLName xml.Name `xml:"xml"`
	Encrypt string   `xml:"Encrypt"`
}
