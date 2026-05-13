package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

const (
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorReset  = "\033[0m"
)

// 单个server块的配置（协议、端口、路径）
type ServerConfig struct {
	HasSSL bool     // 是否启用SSL（决定协议http/https）
	Ports  []string // 对外监听端口（去重）
	Paths  []string // 对外可访问路径（去重）
}

// 最终CSV输出结构体
type NginxResult struct {
	FileName   string // 完整原始文件名（如XX公司-1.1.1.1-nginx.conf）
	InternalIP string // 内网IP
	ExternalIP string // 对外映射IP
	Protocol   string // 协议（http/https）
	Port       string // 对外监听端口
	Path       string // 对外可访问路径（空表示无额外路径）
	FullURL    string // 拼接后的完整URL
}

// 全局正则表达式
var (
	// 匹配location指令的路径
	locationPathRegex = regexp.MustCompile(`(?m)^\s*location(?:\s+.*)?\s+([^\s{]+)`)
	// 匹配文件名中任意位置的合法IPv4地址
	ipRegex = regexp.MustCompile(`([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`)
	// 匹配SSL证书配
	sslCertRegex = regexp.MustCompile(`ssl_certificate(?:_key)?\s+`)
)

// 读取CSV文件并创建内网和外网IP之间的映射
func loadIPMapping(filePath string) (map[string]string, error) {
	ipMapping := make(map[string]string)
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf(ColorRed+"[!]失败：无法打开IP映射文件 %s ：%v"+ColorReset, filePath, err)
	}
	defer file.Close()

	// 处理可能的非UTF-8编码，如GBK
	utf8Reader := transform.NewReader(file, simplifiedchinese.GBK.NewDecoder())
	reader := csv.NewReader(utf8Reader)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1 // 允许每行有可变数量的字段

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf(ColorRed+"[!]失败：读取CSV记录 %s 时出错 %v"+ColorReset, filePath, err)
	}

	if len(records) == 0 {
		return ipMapping, nil // 文件为空，返回空映射
	}

	var headerRowIndex = -1
	var internalIPIndex, externalIPIndex = -1, -1

	// 查找包含“内网服务池”和“对外映射”的表头行
	for i, record := range records {
		tempInternalIdx, tempExternalIdx := -1, -1
		for j, colName := range record {
			if strings.Contains(colName, "内网服务池") {
				tempInternalIdx = j
			}
			if strings.Contains(colName, "对外映射") {
				tempExternalIdx = j
			}
		}
		// 如果在当前行同时找到两列，则视为表头
		if tempInternalIdx != -1 && tempExternalIdx != -1 {
			headerRowIndex = i
			internalIPIndex = tempInternalIdx
			externalIPIndex = tempExternalIdx
			break // 找到第一个匹配的表头后即停止
		}
	}

	if headerRowIndex == -1 {
		fmt.Println(ColorYellow + "[?]警告：在IP映射文件中未找到有效的表头，将跳过IP替换" + ColorReset)
		return ipMapping, nil
	}

	// 处理表头之后的数据行
	ipRegexInCell := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	for i := headerRowIndex + 1; i < len(records); i++ {
		record := records[i]
		if len(record) > internalIPIndex && len(record) > externalIPIndex {
			internalIPCell := record[internalIPIndex]
			externalIPCell := record[externalIPIndex]

			// 从可能包含中文的单元格中提取IP
			internalIPs := ipRegexInCell.FindAllString(internalIPCell, -1)
			externalIPs := ipRegexInCell.FindAllString(externalIPCell, -1)

			if len(internalIPs) > 0 && len(externalIPs) > 0 {
				// 为找到的第一个IP对创建映射
				ipMapping[internalIPs[0]] = externalIPs[0]
			}
		}
	}

	return ipMapping, nil
}

// 从复杂文件名中提取第一个有效的IPv4地址
func extractIPFromFileName(fileName string) (string, error) {
	matches := ipRegex.FindAllStringSubmatch(fileName, -1)
	if len(matches) == 0 {
		return "", fmt.Errorf(ColorRed+"[!]失败：文件名 %s 中未找到IP地址"+ColorReset, fileName)
	}

	for _, match := range matches {
		ipStr := match[1]
		if net.ParseIP(ipStr) != nil {
			return ipStr, nil
		}
	}

	return "", fmt.Errorf(ColorRed+"[!]失败：文件名 %s 中无有效的IPv4地址"+ColorReset, fileName)
}

// 验证端口是否在1-65535合法范围
func isValidPort(portStr string) bool {
	port := 0
	fmt.Sscan(portStr, &port)
	return port >= 1 && port <= 65535
}

// 解析单个Nginx配置文件，按server块分组返回配置
func parseNginxConfig(filePath string) ([]ServerConfig, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf(ColorRed+"[!]失败：读取Nginx配置文件 %s 时出错：%v"+ColorReset, filePath, err)
	}

	contentStr := string(content)
	re := regexp.MustCompile("(?m)#.*$")
	contentStr = re.ReplaceAllString(contentStr, "")

	var serverConfigs []ServerConfig

	cursor := 0
	for {
		startServer := strings.Index(contentStr[cursor:], "server")
		if startServer == -1 {
			break
		}
		startServer += cursor

		startBrace := strings.Index(contentStr[startServer:], "{")
		if startBrace == -1 {
			break
		}
		startBrace += startServer

		braceCount := 1
		endBrace := -1

		for i := startBrace + 1; i < len(contentStr); i++ {
			switch contentStr[i] {
			case '{':
				braceCount++
			case '}':
				braceCount--
			}
			if braceCount == 0 {
				endBrace = i
				break
			}
		}

		if endBrace == -1 {
			cursor = startServer + 6
			continue
		}

		serverBlockContent := contentStr[startBrace+1 : endBrace]
		cursor = endBrace + 1

		var currentServer ServerConfig
		portSet := make(map[string]bool)
		pathSet := make(map[string]bool)

		lines := strings.Split(serverBlockContent, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			if sslCertRegex.MatchString(line) {
				currentServer.HasSSL = true
			}

			if strings.HasPrefix(line, "listen") {
				r := regexp.MustCompile(`\d+`)
				nums := r.FindAllString(line, -1)
				if len(nums) > 0 {
					port := nums[len(nums)-1]
					if isValidPort(port) && !portSet[port] {
						portSet[port] = true
					}
				}
			}

			if strings.HasPrefix(line, "location") {
				pathMatches := locationPathRegex.FindStringSubmatch(line)
				if len(pathMatches) >= 2 {
					path := pathMatches[1]
					if strings.HasPrefix(path, "/") && !pathSet[path] {
						pathSet[path] = true
					}
				}
			}
		}

		for port := range portSet {
			currentServer.Ports = append(currentServer.Ports, port)
		}
		for path := range pathSet {
			currentServer.Paths = append(currentServer.Paths, path)
		}

		if len(currentServer.Ports) > 0 {
			serverConfigs = append(serverConfigs, currentServer)
		}
	}

	return serverConfigs, nil
}

// generateResults 生成最终结果（文件名称、内网IP、对外映射IP、协议、对外监听端口、对外可访问路径、完整URL）
func generateResults(fileName, internalIP string, serverConfigs []ServerConfig, ipMapping map[string]string) []NginxResult {
	var results []NginxResult

	externalIP := ipMapping[internalIP]

	displayIP := internalIP
	if externalIP != "" {
		displayIP = externalIP
	}

	for _, server := range serverConfigs {
		// 跳过无有效端口的server块
		if len(server.Ports) == 0 {
			continue
		}

		// 确定协议：有SSL→https，无→http
		protocol := "http"
		if server.HasSSL {
			protocol = "https"
		}

		// 无对外路径（每个端口生成一个无路径URL）
		if len(server.Paths) == 0 {
			for _, port := range server.Ports {
				fullURL := fmt.Sprintf("%s://%s:%s", protocol, displayIP, port)
				results = append(results, NginxResult{
					FileName:   fileName,
					InternalIP: internalIP,
					ExternalIP: externalIP,
					Protocol:   protocol,
					Port:       port,
					Path:       "",
					FullURL:    fullURL,
				})
			}
			continue
		}

		// 有对外路径（端口+路径组合生成URL）
		for _, port := range server.Ports {
			for _, path := range server.Paths {
				// 路径优化：去除末尾/，避免拼接后出现//（如/api/→/api）
				cleanPath := strings.TrimSuffix(path, "/")
				// 根路径特殊处理：cleanPath为空时设为/，拼接为 http://ip:port/
				if cleanPath == "" {
					cleanPath = "/"
				}
				fullURL := fmt.Sprintf("%s://%s:%s%s", protocol, displayIP, port, cleanPath)
				results = append(results, NginxResult{
					FileName:   fileName,
					InternalIP: internalIP,
					ExternalIP: externalIP,
					Protocol:   protocol,
					Port:       port,
					Path:       cleanPath,
					FullURL:    fullURL,
				})
			}
		}
	}

	return results
}

// writeCSV 按需求格式写入CSV
func writeCSV(results []NginxResult, outputPath string) error {
	// 创建CSV文件
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf(ColorRed+"[!]失败：创建文件 %s 时出错 %v"+ColorReset, outputPath, err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 表头（严格按需求顺序）
	headers := []string{"文件名称", "内网IP", "对外映射IP", "协议", "对外监听端口", "对外可访问路径", "完整URL"}
	if err := writer.Write(headers); err != nil {
		return fmt.Errorf(ColorRed+"[!]失败：写入表头 %v 出错"+ColorReset, err)
	}

	// 写入数据行（顺序与表头一致）
	for _, res := range results {
		row := []string{
			res.FileName,
			res.InternalIP,
			res.ExternalIP,
			res.Protocol,
			res.Port,
			res.Path,
			res.FullURL,
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf(ColorRed+"[!]失败：写入数据失败（文件：%s，端口：%s）：%v"+ColorReset, res.FileName, res.Port, err)
		}
	}

	return nil
}

// 遍历目录下所有.conf后缀文件（含子目录）
func walkNginxFiles(dir string) ([]string, error) {
	var confFiles []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// 仅处理普通文件且后缀为.conf
		if !info.IsDir() && filepath.Ext(path) == ".conf" {
			confFiles = append(confFiles, path)
		}
		return nil
	})
	return confFiles, err
}

func main() {
	// 命令行标志及其描述和默认值
	var (
		confDir   = flag.String("dir", ".", "Nginx配置文件目录（默认为当前目录）")
		ipMapFile = flag.String("load", "template.csv", "IP映射CSV文件路径（默认为template.csv）")
		outputCSV = flag.String("out", "results.csv", "输出CSV文件路径（默认为results.csv）")
	)
	flag.Parse()

	// 加载IP映射
	fmt.Printf("正在加载IP映射文件 [%s]...\n", *ipMapFile)
	ipMapping, err := loadIPMapping(*ipMapFile)
	if err != nil {
		fmt.Printf(ColorRed+"[!]失败：加载IP映射文件 %s 时出错 %v"+ColorReset+"\n", *ipMapFile, err)
		os.Exit(1)
	}
	fmt.Printf("[*]成功加载 %d 条IP映射关系\n", len(ipMapping))

	// 在目标目录中查找所有.conf文件
	fmt.Printf("正在扫描目录 [%s] 下的Nginx配置文件...\n", *confDir)
	confFiles, err := walkNginxFiles(*confDir)
	if err != nil {
		fmt.Printf(ColorRed+"[!]失败：目录扫描 %s 时出错 %v"+ColorReset+"\n", *confDir, err)
		os.Exit(1)
	}
	if len(confFiles) == 0 {
		fmt.Println("未找到.conf配置文件，程序退出")
		os.Exit(0)
	}
	fmt.Printf("发现 %d 个配置文件，开始并发解析...\n", len(confFiles))

	// 为提高效率，并发解析文件
	var (
		wg         sync.WaitGroup
		allResults []NginxResult
		mu         sync.Mutex // 用于在并发写入期间保护结果切片的互斥锁
	)

	for _, file := range confFiles {
		wg.Add(1)
		go func(filePath string) {
			defer wg.Done()
			fileName := filepath.Base(filePath)
			fmt.Printf("正在处理文件：%s\n", fileName)

			// 从文件名提取IP
			ip, err := extractIPFromFileName(fileName)
			if err != nil {
				fmt.Printf(ColorYellow+"[?]警告：从 %s 提取IP失败，已跳过：%v"+ColorReset+"\n", fileName, err)
				return
			}

			// 解析服务器块配置（SSL、端口、路径）
			serverConfigs, err := parseNginxConfig(filePath)
			if err != nil {
				fmt.Printf(ColorYellow+"[?]警告：解析 %s 失败，已跳过：%v"+ColorReset+"\n", fileName, err)
				return
			}

			// 生成结果，包括IP替换
			fileResults := generateResults(fileName, ip, serverConfigs, ipMapping)
			if len(fileResults) == 0 {
				fmt.Printf(ColorYellow+"[?]警告：%s 中未找到有效配置（无端口/路径），已跳过"+ColorReset+"\n", fileName)
				return
			}

			// 安全地附加到全局结果切片
			mu.Lock()
			allResults = append(allResults, fileResults...)
			mu.Unlock()

			fmt.Printf("[*]成功：从 %s 解析出 %d 条有效记录\n", fileName, len(fileResults))
		}(file)
	}

	wg.Wait()
	fmt.Printf("\n所有文件处理完毕，共生成 %d 条结果\n", len(allResults))

	// 将结果写入CSV文件
	if err := writeCSV(allResults, *outputCSV); err != nil {
		fmt.Printf(ColorRed+"[!]失败：写入CSV文件 %s 时出错 %v"+ColorReset+"\n", *outputCSV, err)
		os.Exit(1)
	}
	fmt.Printf("结果已保存至：%s\n", *outputCSV)
}
