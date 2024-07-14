package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
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

func getResultList(content string) []LatencyResult {
	var wg sync.WaitGroup
	ipList := strings.Split(strings.Trim(content, "\n"), "\n")
	resultChan := make(chan LatencyResult, len(ipList))

	for _, ip := range ipList {
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
			logStr := fmt.Sprintf("所选ip-%s的丢包率为: %s, 延时为: %dms", item.IP, lossRateStr, item.Latency)
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
			resp.Body.Close()
			log.Info(logStr)
			retry++
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
			resp.Body.Close()
			log.Info(logStr)
			retry++
			continue
		}
		resp.Body.Close()

		var dnsRecords map[string]interface{}
		json.Unmarshal(body, &dnsRecords)
		resultList := dnsRecords["result"].([]interface{})
		rid := ""
		proxiable := false
		for _, record := range resultList {
			if record.(map[string]interface{})["type"].(string) == "A" {
				rid = record.(map[string]interface{})["id"].(string)
				proxiable = record.(map[string]interface{})["proxiable"].(bool)
				break
			}
		}

		params := map[string]interface{}{
			"id":        zid,
			"type":      "A",
			"name":      fmt.Sprintf("%s.%s", name, domain),
			"content":   ip,
			"proxiable": proxiable,
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
			resp.Body.Close()
			log.Info(logStr)
			break
		} else {
			resp.Body.Close()
			retry++
		}
	}
	if retry >= 5 {
		logStr := fmt.Sprintf("%s.%s的ip更新失败", name, domain)
		log.Info(logStr)
	}
}

func handleMain(config Config, domainInfo []string, retry bool) {
	url := fmt.Sprintf("%s.%s", domainInfo[0], domainInfo[1])
	result := getLatency(url, 10)
	if result.Latency > 200 || retry {
		if len(resultList) == 0 {
			resp, err := http.Get("https://zip.baipiao.eu.org")
			if err != nil {
				logStr := fmt.Sprintf("下载ZIP文件错误: %s", err)
				log.Info(logStr)
				return
			}
			buf, err := io.ReadAll(resp.Body)
			if err != nil {
				logStr := fmt.Sprintf("读取ZIP文件错误: %s", err)
				resp.Body.Close()
				log.Info(logStr)
				return
			}
			resp.Body.Close()

			z, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
			if err != nil {
				logStr := fmt.Sprintf("打开ZIP文件错误: %s", err)
				log.Info(logStr)
				return
			}

			for _, file := range z.File {
				var content []byte
				fileName := strings.TrimSuffix(file.Name, ".txt")
				parts := strings.Split(fileName, "-")
				if len(domainInfo) == 3 {
					newParts := strings.Split(domainInfo[2], "-")
					if (parts[0] == newParts[0] || newParts[0] == "*") && (parts[1] == newParts[1] || newParts[1] == "*") && (parts[2] == newParts[2] || newParts[2] == "*") {
						tls = parts[1]
						port = parts[2]
						rc, err := file.Open()
						if err != nil {
							logStr := fmt.Sprintf("无法打开ZIP中的文件: %s", err)
							log.Info(logStr)
							return
						}
						content, err = io.ReadAll(rc) // 赋值 content
						rc.Close()
						if err != nil {
							logStr := fmt.Sprintf("无法读取ZIP中的文件: %s", err)
							log.Info(logStr)
							return
						}
					}
				} else {
					tls = parts[1]
					port = parts[2]
					rc, err := file.Open()
					if err != nil {
						logStr := fmt.Sprintf("无法打开ZIP中的文件: %s", err)
						log.Info(logStr)
						return
					}
					content, err = io.ReadAll(rc) // 赋值 content
					rc.Close()
					if err != nil {
						logStr := fmt.Sprintf("无法读取ZIP中的文件: %s", err)
						log.Info(logStr)
						return
					}
				}
				if len(content) > 0 {
					resultList = getResultList(string(content))
				}
			}
		}

		if globalIP == "" && len(resultList) > 0 {
			globalIP = getIP(resultList)
		}
		if globalIP != "" {
			uploadIP(globalIP, domainInfo[0], domainInfo[1], config.Email, config.Key)
			var protocol string
			if tls == string('0') {
				protocol = "http"
			} else {
				protocol = "https"
			}
			url = fmt.Sprintf("%s.%s", domainInfo[0], domainInfo[1])

			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()

			breakSignal := false
			retryHandleMain := false
			maxExecutions := 120
			for i := 0; i < maxExecutions; i++ {
				<-ticker.C
				ips, err := net.LookupIP(url)
				if err != nil {
					logStr := fmt.Sprintf("获取 IP 地址失败: %s", err)
					log.Info(logStr)
					continue
				}

				for _, newIP := range ips {
					ipStr := newIP.String()
					if ipStr == globalIP {
						client := http.Client{
							Timeout: 30 * time.Second,
						}
						func() {
							resp, err := client.Get(fmt.Sprintf("%s://%s:%s", protocol, url, port))
							if err != nil {
								logStr := fmt.Sprintf("访问%s失败: %s", url, err)
								log.Info(logStr)
								blockList[ipStr] = true
								retryHandleMain = true
								breakSignal = true
								globalIP = ""
								return
							}
							defer resp.Body.Close()

							statusCode := resp.StatusCode
							if statusCode >= 200 && statusCode < 300 {
								logStr := fmt.Sprintf("访问%s正常, 更新完成", url)
								log.Info(logStr)
								breakSignal = true
								return
							} else {
								logStr := fmt.Sprintf("访问%s失败, 程序继续", url)
								log.Info(logStr)
								globalIP = ""
								blockList[ipStr] = true
								breakSignal = true
								retryHandleMain = true
							}
						}()
					} else {
						logStr := fmt.Sprintf("域名%s的IP为%s, 暂未更新, 等待5s后重试", url, ipStr)
						log.Info(logStr)
					}
					if breakSignal {
						break
					}
				}
				if breakSignal {
					break
				}
			}
			if retryHandleMain {
				handleMain(config, domainInfo, true)
			}
		}
	} else {
		logStr := fmt.Sprintf("域名%s的ip延时为%dms小于200ms, 未更新", url, result.Latency)
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
		handleMain(config, domainInfo, false)
	}
}
