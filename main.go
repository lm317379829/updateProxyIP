package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-ping/ping"
	log "github.com/sirupsen/logrus"
)

var tls string
var port string
var globalIP string
var resultList []LatencyResult
var blockList = make(map[string]bool)

type DomainInfo struct {
	Subdomain string   `json:"subdomain"`
	Domain    string   `json:"domain"`
	Ports     []string `json:"ports"`
}

type Config struct {
	Email       string     `json:"email"`
	Key         string     `json:"key"`
	DomainInfos [][]string `json:"domainInfos"`
}

type LatencyResult struct {
	Latency  int     `json:"latency"`
	IP       string  `json:"ip"`
	LossRate float64 `json:"lossRate"`
}

func getLatency(ip string, count int) LatencyResult {
	var latency int = 9999
	var lossRate float64 = 1.00
	var lossRateStr string

	pinger, err := ping.NewPinger(ip)
	if err != nil {
		logStr := fmt.Sprintf("ping 错误: %s", err)
		log.Info(logStr)
		return LatencyResult{Latency: 9999, IP: ip, LossRate: 1.00}
	}
	pinger.SetPrivileged(true)
	pinger.Count = count
	pinger.Timeout = 1000 * 1000 * 1000 // 1秒
	pinger.Run()
	stats := pinger.Statistics()
	if stats.PacketLoss < 35 {
		latency = int(stats.AvgRtt.Seconds() * 1000)
		lossRateStr = fmt.Sprintf("%.2f", stats.PacketLoss)
		lossRate, _ = strconv.ParseFloat(lossRateStr, 64)
		return LatencyResult{Latency: latency, IP: ip, LossRate: lossRate}
	} else {
		return LatencyResult{Latency: latency, IP: ip, LossRate: lossRate}
	}
}

func getResultList(content string, blockArea string) []LatencyResult {
	var wg sync.WaitGroup
	ipList := strings.Split(strings.Trim(content, "\n"), "\n")
	resultChan := make(chan LatencyResult, len(ipList))

	for _, ip := range ipList {
		if strings.Contains(ip, "#") {
			parts := strings.Split(ip, "#")
			ip = parts[0]
			if strings.Contains(parts[1], blockArea) {
				continue
			}
		}
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			resultChan <- getLatency(ip, 4)
		}(ip)
	}

	wg.Wait()
	close(resultChan)

	for result := range resultChan {
		if result.Latency > 999 {
			continue
		}
		resultList = append(resultList, result)
	}
	return resultList
}

func getIP(resultList []LatencyResult) string {
	sort.Slice(resultList, func(i, j int) bool {
		return resultList[i].Latency < resultList[j].Latency
	})
	for _, item := range resultList {
		if _, exists := blockList[item.IP]; !exists && item.LossRate <= 0.35 {
			lossRateStr := fmt.Sprintf("%.2f", item.LossRate)
			logStr := fmt.Sprintf("所选ip-%s的丢包率为: %s, 延时为：%dms", item.IP, lossRateStr, item.Latency)
			log.Info(logStr)
			return item.IP
		}
	}
	return ""
}

func uploadIP(ip, name string, domain string, email string, key string) {
	retry := 0
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones?name=%s", domain)
	header := map[string]string{
		"User-Agent":   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/94.0.4606.54 Safari/537.36",
		"Content-Type": "application/json",
		"X-Auth-Email": email,
		"X-Auth-Key":   key,
	}

	client := &http.Client{Timeout: 15 * time.Second}
	for retry < 5 {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			logStr := fmt.Sprintf("第%d次访问 %s 失败: %s", retry, url, err)
			log.Info(logStr)
			retry++
			continue
		}
		for key, value := range header {
			req.Header.Set(key, value)
		}

		resp, err := client.Do(req)
		if err != nil {
			logStr := fmt.Sprintf("第%d次创建 %s 访问出错: %s", retry, url, err)
			log.Info(logStr)
			retry++
			continue
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			logStr := fmt.Sprintf("第%d次读取 %s 访问结果出错: %s", retry, url, err)
			log.Info(logStr)
			retry++
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		var result map[string]interface{}
		json.Unmarshal(body, &result)
		zid := result["result"].([]interface{})[0].(map[string]interface{})["id"].(string)

		url = fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?name=%s.%s", zid, name, domain)
		req, err = http.NewRequest("GET", url, nil)
		if err != nil {
			logStr := fmt.Sprintf("第%d次访问 %s 失败: %s", retry, url, err)
			log.Info(logStr)
			retry++
			continue
		}
		for key, value := range header {
			req.Header.Set(key, value)
		}

		resp, err = client.Do(req)
		if err != nil {
			logStr := fmt.Sprintf("第%d次创建 %s 访问出错: %s", retry, url, err)
			log.Info(logStr)
			retry++
			continue
		}

		body, err = io.ReadAll(resp.Body)
		if err != nil {
			logStr := fmt.Sprintf("第%d次读取 %s 访问结果出错: %s", retry, url, err)
			log.Info(logStr)
			retry++
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		var dnsRecords map[string]interface{}
		json.Unmarshal(body, &dnsRecords)
		resultList := dnsRecords["result"].([]interface{})
		rid := ""
		for _, record := range resultList {
			if record.(map[string]interface{})["type"].(string) == "A" {
				rid = record.(map[string]interface{})["id"].(string)
				break
			}
		}

		params := map[string]interface{}{
			"id":      zid,
			"type":    "A",
			"name":    fmt.Sprintf("%s.%s", name, domain),
			"content": ip,
		}
		if rid == "" {
			continue
		}

		url = fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", zid, rid)
		data, _ := json.Marshal(params)
		req, err = http.NewRequest("PUT", url, bytes.NewBuffer(data))
		if err != nil {
			logStr := fmt.Sprintf("第%d次访问 %s 失败: %s", retry, url, err)
			log.Info(logStr)
			retry++
			continue
		}
		for key, value := range header {
			req.Header.Set(key, value)
		}

		resp, err = client.Do(req)
		if err != nil {
			logStr := fmt.Sprintf("第%d次创建 %s 访问出错: %s", retry, url, err)
			log.Info(logStr)
			retry++
			continue
		}

		if resp.StatusCode == 200 {
			logStr := fmt.Sprintf("成功更新%s.%s的ip为%s", name, domain, ip)
			log.Info(logStr)
			break
		} else {
			retry++
		}
	}
	if retry >= 5 {
		logStr := fmt.Sprintf("%s.%s的ip更新失败", name, domain)
		log.Info(logStr)
	}
}

func handleMain(config Config, domainInfo []string) {
	url := fmt.Sprintf("%s.%s", domainInfo[0], domainInfo[1])
	result := getLatency(url, 10)
	if result.Latency > 200 {
		if len(resultList) == 0 {
			resp, err := http.Get("https://ipdb.api.030101.xyz/?type=bestcf&country=true")
			if err != nil {
				logStr := fmt.Sprintf("下载优选IP列表错误: %s", err)
				log.Info(logStr)
				return
			}
			defer resp.Body.Close()
			buf, err := io.ReadAll(resp.Body)
			if err != nil {
				logStr := fmt.Sprintf("读取优选IP列表错误: %s", err)
				log.Info(logStr)
				return
			}
			var content string
			content = string(buf)
			if len(content) > 0 {
				if len(domainInfo) == 3 {
					resultList = getResultList(string(content), domainInfo[2])
				} else {
					resultList = getResultList(string(content), "")
				}
			}
		}
		if globalIP == "" && len(resultList) > 0 {
			globalIP = getIP(resultList)
		}

		if globalIP != "" {
			uploadIP(globalIP, domainInfo[0], domainInfo[1], config.Email, config.Key)
		}
	} else {
		logStr := fmt.Sprintf("域名%s的ip延时为%dms小于200ms，未更新", url, result.Latency)
		log.Info(logStr)
	}

}

func main() {
	// 定义命令行参数
	filePath := flag.String("file", "config.json", "文件路径和名称")

	// 解析命令行参数
	flag.Parse()

	// 设置日志输出
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)

	// 打开文件
	file, err := os.Open(*filePath)
	if err != nil {
		logStr := fmt.Sprintf("无法打开配置文件: %s", err)
		log.Info(logStr)
		return
	}
	defer file.Close()

	// 读取文件内容
	bytes, err := io.ReadAll(file)
	if err != nil {
		logStr := fmt.Sprintf("无法读取配置文件: %s", err)
		log.Info(logStr)
		return
	}

	// 解析 JSON 文件内容
	var config Config
	if err := json.Unmarshal(bytes, &config); err != nil {
		logStr := fmt.Sprintf("无法解析配置文件: %s", err)
		log.Info(logStr)
		return
	}

	for _, domainInfo := range config.DomainInfos {
		handleMain(config, domainInfo)
	}
}
