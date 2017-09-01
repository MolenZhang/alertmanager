// Copyright 2015 Prometheus Team
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

package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"net/http"
	_ "net/http/pprof" // Comment this line to disable pprof endpoint.
	"os"
	"path/filepath"
	"sync"

	c "github.com/prometheus/alertmanager/config"
	//t "github.com/prometheus/alertmanager/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/route"
)

var (
	CFile *string
)

const (
	email   string = "email"   //邮件
	message string = "message" //短信
	wechat  string = "wechat"  //微信

	webhookURL string = "http://localhost:9093/mobile"
	msgSendAPI string = "http://10.161.35.65:1821/octopus/rest/api/message/send/prometheus"
)

func serveAsset(w http.ResponseWriter, req *http.Request, fp string) {
	info, err := AssetInfo(fp)
	if err != nil {
		log.Warn("Could not get file: ", err)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	file, err := Asset(fp)
	if err != nil {
		if err != io.EOF {
			log.With("file", fp).Warn("Could not get file: ", err)
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.ServeContent(w, req, info.Name(), info.ModTime(), bytes.NewReader(file))
}

// Register registers handlers to serve files for the web interface.
func Register(r *route.Router, reloadCh chan<- struct{}) {
	ihf := prometheus.InstrumentHandlerFunc

	r.Get("/metrics", prometheus.Handler().ServeHTTP)

	r.Get("/", ihf("index", func(w http.ResponseWriter, req *http.Request) {
		serveAsset(w, req, "ui/app/index.html")
	}))

	r.Get("/script.js", ihf("app", func(w http.ResponseWriter, req *http.Request) {
		serveAsset(w, req, "ui/app/script.js")
	}))

	r.Get("/favicon.ico", ihf("app", func(w http.ResponseWriter, req *http.Request) {
		serveAsset(w, req, "ui/app/favicon.ico")
	}))

	r.Get("/lib/*filepath", ihf("lib_files",
		func(w http.ResponseWriter, req *http.Request) {
			fp := route.Param(req.Context(), "filepath")
			serveAsset(w, req, filepath.Join("ui/lib", fp))
		},
	))

	/*	r.Post("/-/reload", func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte("Reloading configuration file..."))
		reloadCh <- struct{}{}
	})*/
	r.Post("/-/reload", func(w http.ResponseWriter, r *http.Request) {
		bcmCreReload(w, r, reloadCh)
	})

	r.Del("/-/reload", func(w http.ResponseWriter, r *http.Request) {
		bcmDelReload(w, r, reloadCh)
	})

	r.Put("/-/reload", func(w http.ResponseWriter, r *http.Request) {
		bcmUpdReload(w, r, reloadCh)
	})
	r.Get("/debug/*subpath", http.DefaultServeMux.ServeHTTP)
	r.Post("/debug/*subpath", http.DefaultServeMux.ServeHTTP)

	//for message with wangke`s API
	r.Post("/mobile", func(w http.ResponseWriter, r *http.Request) {
		sendAlertWithMsg(w, r)
	})
}

//定义短信参数接口
type alertMsg struct {
	Receiver string       `json:"receiver"`
	Status   string       `json:"status"`
	Alerts   []AlertsInfo `json:"alerts"`
}

type AlertsInfo struct {
	Status      string          `json:"status"`
	Labels      LabelsInfo      `json:"labels"`
	Annotations AnnotationsInfo `json:"annotations"`
	StartsAt    string          `json:"startAt"`
}

type AnnotationsInfo struct {
	Description string `json:description"`
	Summary     string `json:summary`
}

type LabelsInfo struct {
	AlertName string `json:"alertname"`
	Instance  string `json:"instance"`
	Job       string `json:"job"`
	Severity  string `json:"severity"`
}

type chanInfo struct {
	pNum string
	aMsg alertMsg
}

var MsgChan chan chanInfo

func Init() {
	MsgChan = make(chan chanInfo)
}

func sendAlertWithMsg(w http.ResponseWriter, r *http.Request) {
	log.Infoln("----------进入短信发送---------")

	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Errorln("error ocurred:", err)
	}
	defer r.Body.Close()

	var alert alertMsg
	json.Unmarshal(b, &alert)

	log.Infoln("短信发送时 获取的报警信息:", alert)
	number := getNumFromMap(alert.Receiver)
	log.Infoln("获取的电话号码是:", number)

	//发信号 给短信处理接口
	c := chanInfo{
		pNum: number,
		aMsg: alert,
	}

	MsgChan <- c

}

func SendToMobile(msg chanInfo) {
	log.Infoln("接收到的报警信息是:", msg)

	msgInfo := fmt.Sprintf(`监控状态:%s
监控名称:%s
详细信息:%s,%s。`, msg.aMsg.Status,
		msg.aMsg.Alerts[0].Labels.AlertName,
		msg.aMsg.Alerts[0].Annotations.Description,
		msg.aMsg.Alerts[0].Annotations.Summary)

	var (
		sendMsg = struct {
			Mobiles []string `json:"mobiles"`
			Content string   `json:"content"`
		}{
			Mobiles: []string{msg.pNum},
			Content: msgInfo,
		}
		req = Request{
			Content: sendMsg,
			Type:    POST,
			URL:     msgSendAPI,
		}

		resp respMsg
	)

	data, err := req.SendRequestByJSON()
	if err != nil {
		log.Infoln("短信发送时出错:", err)
	}

	json.Unmarshal(data, &resp)
	log.Infoln("短信发送的返回结果:", resp)
}

type receiverInfo struct {
	OldReceiver string
	NewReceiver string
	SvcName     string
}

func bcmUpdReload(w http.ResponseWriter, r *http.Request, reloadCh chan<- struct{}) {

	mtx := sync.RWMutex{}
	mtx.RLock()
	defer mtx.RUnlock()
	log.Infoln("bcm开始更新相应配置...")
	var (
		resp    respMsg
		recInfo struct {
			LabelInfo receiverInfo      `json:"labelInfo"`
			AlertType map[string]string `json:"alertType"`
		}
	)

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Infoln("解析prom发来参数出错:", err)
		resp.respFail(w, "解析prom发来参数出错")
		return
	}
	json.Unmarshal(data, &recInfo)
	log.Infoln("更新时 接收到的信息是:", recInfo)

	conf, _, _ := c.LoadFile(*CFile)
	log.Infoln("程序中现有的配置文件为:", conf)
	updAlertParam(conf, recInfo.LabelInfo, recInfo.AlertType)

	//重启alertmanager配置
	reloadCh <- struct{}{}
	log.Infoln("alertmanager重启成功!")
	resp.respSucc(w, "操作成功")
}
func updAlertParam(conf *c.Config, labInfo receiverInfo, alertType map[string]string) {

	//更新标签匹配信息
	for index, route := range conf.Route.Routes {
		if route.Receiver == labInfo.OldReceiver {
			conf.Route.Routes[index].Receiver = labInfo.NewReceiver
			conf.Route.Routes[index].Match[labInfo.SvcName+"_receiver"] = labInfo.NewReceiver
			break
		}
	}

	//更新告警接收者信息
	for index, receiver := range conf.Receivers {
		if receiver.Name == labInfo.OldReceiver {
			conf.Receivers[index].Name = labInfo.NewReceiver
			for k, v := range alertType {
				if k == email {

					//先删除旧的信息
					conf.Receivers[index].EmailConfigs = append(conf.Receivers[index].EmailConfigs[:0],
						conf.Receivers[index].EmailConfigs[1:]...)
					//conf.Receivers[index].EmailConfigs[0].To = v
					emailCfg := &c.EmailConfig{
						To: v,
					}
					conf.Receivers[index].EmailConfigs = append(conf.Receivers[index].EmailConfigs,
						emailCfg)
				}

				if k == message {
					//先删除旧的信息
					conf.Receivers[index].WebhookConfigs = append(conf.Receivers[index].WebhookConfigs[:0],
						conf.Receivers[index].WebhookConfigs[1:]...)

					webhookCfg := &c.WebhookConfig{
						URL: webhookURL,
					}
					conf.Receivers[index].WebhookConfigs = append(conf.Receivers[index].WebhookConfigs,
						webhookCfg)
					//更新号码map
					//先删除旧信息，再添加新信息
					log.Infoln("更新前的number map:", getAllNumFromMap())
					delNumFromMap(labInfo.OldReceiver)

					addNumToMap(labInfo.NewReceiver, v)
					log.Infoln("更新后的number map:", getAllNumFromMap())
				}
			}

		}
	}
	log.Infoln("程序中修改后的配置文件为:", conf)

	out, _ := yaml.Marshal(&conf)

	os.Remove("/etc/alertmanager.yaml")
	f, _ := os.OpenFile("/etc/alertmanager.yaml", os.O_CREATE|os.O_RDWR, 0600)
	f.Write(out)

	log.Infoln("解析成YAML的配置数据:", string(out))

}

func bcmDelReload(w http.ResponseWriter, r *http.Request, reloadCh chan<- struct{}) {
	mtx := sync.RWMutex{}
	mtx.RLock()
	defer mtx.RUnlock()
	log.Infoln("bcm开始删除相应配置...")
	var (
		resp    respMsg
		recInfo struct {
			LabelInfo recLabel
			//AlertType map[string]string
		}
	)

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Infoln("解析prom发来参数出错:", err)
		resp.respFail(w, "解析prom发来参数出错")
		return
	}
	json.Unmarshal(data, &recInfo)
	log.Infoln("&&&解析到prom发来的数据是&&&:", recInfo)
	//删除alertmanager配置文件中相关的参数
	conf, _, _ := c.LoadFile(*CFile)
	log.Infoln("程序中删除前的配置文件为:", conf)

	//删除之前添加的标签信息 和 告警接收者
	delAlertParam(conf, recInfo.LabelInfo)

	//重启alertmanager配置
	reloadCh <- struct{}{}
	log.Infoln("alertmanager重启成功!")
	resp.respSucc(w, "操作成功")

}

func delAlertParam(conf *c.Config, recInfo recLabel) {
	//删除匹配标签参数
	delRoute := &c.Route{
		Match:    recInfo.Match,
		Receiver: recInfo.Receiver,
	}
	for k, route := range conf.Route.Routes {
		if route.Receiver == delRoute.Receiver {
			conf.Route.Routes = append(conf.Route.Routes[:k], conf.Route.Routes[k+1:]...)
			break
		}
	}
	//删除接收者信息
	/*	delEmailCfg := &c.EmailConfig{
		To: recInfo.AlertType[EMAIL],
		Headers: map[string]string{
			"Subject": "[WARN] AlertManager报警邮件",
		},
	}*/
	delReceiver := &c.Receiver{
		Name: recInfo.Receiver,
	}
	//	delReceiver.EmailConfigs = append(delReceiver.EmailConfigs, delEmailCfg)

	for k, receiver := range conf.Receivers {
		log.Infoln("***程序中原本存在的接收者信息是:", receiver)
		if receiver.Name == delReceiver.Name {
			conf.Receivers = append(conf.Receivers[:k], conf.Receivers[k+1:]...)
		}
	}
	log.Infoln("程序中删除后的配置文件为:", conf)

	out, _ := yaml.Marshal(&conf)

	os.RemoveAll(*CFile)
	f, _ := os.OpenFile(*CFile, os.O_CREATE|os.O_RDWR, 0600)
	f.Write(out)
}

type recLabel struct {
	Match    map[string]string /*`yaml:"match"`*/
	Receiver string            /*`yaml:"receiver"`*/
}

type respMsg struct {
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

func isRouteExists(conf *c.Config, receiver string) bool {
	for _, route := range conf.Route.Routes {
		if route.Receiver == receiver {
			return true
		}
	}
	return false
}

func (r *respMsg) respSucc(w http.ResponseWriter, msg string) {
	r.Status = 200
	r.Msg = msg
	result, _ := json.Marshal(r)
	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

func (r *respMsg) respFail(w http.ResponseWriter, msg string) {
	r.Status = 400
	r.Msg = msg
	result, _ := json.Marshal(r)
	w.Header().Set("Content-Type", "application/json")
	w.Write(result)

}

type recMsg struct {
	LabelInfo recLabel
	AlertType map[string]string
}

func bcmCreReload(w http.ResponseWriter, r *http.Request, reloadCh chan<- struct{}) {
	mtx := sync.RWMutex{}
	mtx.RLock()
	defer mtx.RUnlock()

	log.Infoln("bcm创建相应配置.....")
	var (
		resp    respMsg
		recInfo recMsg
	)

	recInfo.AlertType = make(map[string]string, 0)
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		resp.respFail(w, "解析prom传来数据出错")
		log.Errorln("解析prom传来数据出错:", err)
		return
	}

	json.Unmarshal(data, &recInfo)
	log.Infoln("接受到prom发来得数据:", recInfo)

	conf, _, _ := c.LoadFile(*CFile)
	log.Infoln("程序中现有的配置文件为:", conf)

	//增加标签匹配 和 告警接收者
	addAlertParam(conf, recInfo)

	//重启alertmanager配置
	reloadCh <- struct{}{}
	log.Infoln("alertmanager重启成功!")

	resp.respSucc(w, "操作成功")
}

func addAlertParam(conf *c.Config, recInfo recMsg) {
	//增加邮箱匹配配置
	//如果label存在则不创建
	if false == isRouteExists(conf, recInfo.LabelInfo.Receiver) {
		appendRoute := &c.Route{
			Match:    recInfo.LabelInfo.Match,
			Receiver: recInfo.LabelInfo.Receiver,
		}
		conf.Route.Routes = append(conf.Route.Routes, appendRoute)
	}

	//增加邮件接收者配置，接收者信息唯一
	if false == isReceiversExists(conf.Receivers, recInfo.LabelInfo.Receiver) {

		appendReceiver := &c.Receiver{
			Name: recInfo.LabelInfo.Receiver,
		}
		for mk, mv := range recInfo.AlertType {
			//判断 发送方式 是短信 还是 邮件
			if mk == email {
				recData := &c.EmailConfig{
					To: mv,
					Headers: map[string]string{
						"Subject": "[WARN] AlertManager报警邮件",
					},
				}
				appendReceiver.EmailConfigs = append(appendReceiver.EmailConfigs, recData)
				//conf.Receivers = append(conf.Receivers, appendReceiver)
			}

			if mk == message {
				recData := &c.WebhookConfig{
					URL: webhookURL, //URL http://0.0.0.0:7272/mobile
				}
				appendReceiver.WebhookConfigs = append(appendReceiver.WebhookConfigs, recData)

				//存入接收者-手i机号码 信息到 map
				addNumToMap(recInfo.LabelInfo.Receiver, mv)
				log.Infoln("存入后的号码map:", getAllNumFromMap())

			}
		}
		conf.Receivers = append(conf.Receivers, appendReceiver)

	}

	log.Infoln("程序中修改后的配置文件为:", conf)

	out, _ := yaml.Marshal(&conf)

	os.Remove("/etc/alertmanager.yml")
	f, _ := os.OpenFile("/etc/alertmanager.yaml", os.O_CREATE|os.O_RDWR, 0600)
	f.Write(out)

	log.Infoln("解析成YAML的配置数据:", string(out))

}

//该map主要保存 接收者和对应的电话号码信息 :receiver <--> pNumber
var pNumMap = make(map[string]string, 0)

//存入map
func addNumToMap(rec, num string) {

	pNumMap[rec] = num
}

//获取号码
func getNumFromMap(rec string) string {

	return pNumMap[rec]
}

//获取所有号码
func getAllNumFromMap() map[string]string {
	return pNumMap
}

//删除号码
func delNumFromMap(rec string) {

	delete(pNumMap, rec)
}

//更新号码
func updNumFromMap(rec, num string) {
	pNumMap[rec] = num
}

func isReceiversExists(rec []*c.Receiver, name string) bool {
	for _, r := range rec {
		if r.Name == name {
			return true
		}
	}
	return false

}
