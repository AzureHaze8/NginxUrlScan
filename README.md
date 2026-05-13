# Nginx配置文件违规外联排查工具

## 一、功能说明

本工具用于扫描Nginx配置文件，请先根据提供的公网与内网IP映射关系，自动识别出存在违规外联风险的URL路径，并将结果输出到CSV文件中，便于安全审计和整改。

## 二、文件说明

### 1. 输入文件

#### (1) Nginx配置文件 (.conf)

- **命名规范**: <mark>业务系统-内网IP-年月日.conf</mark>，例如：<mark>示例系统-10.10.10.1-20251127.conf</mark>。

![](images\Snipaste_2025-12-22_17-01-33.png)

- **扫描目录**: 将所有需要扫描的`.conf`文件统一放置在一个任意命名的目录下，通过 `-dir` 参数指定。

![](.\images\Snipaste_2025-12-22_17-06-48.png)

#### (2) IP映射模板 (template.csv)

**作用**: 定义公网IP与内网IP的映射关系。

**格式**:

- 文件必须为CSV格式，编码为UTF-8。
- 必须包含两个表头：<mark>对外映射列在前，填写公网IP地址；内网服务池列在后，填写对应的内网IP地址</mark>，一个公网地址可以对应多个内网IP。
- IP地址为纯IPv4地址，不加入中文、空格等其他字符。

![](.\images\Snipaste_2025-12-22_17-08-00.png)

### (3). 注意：

- -dir参数可读取一整个文件夹下的.conf文件
- 不要删除template.csv文件，同时该文件只有两个标题 对外映射,内网服务池

## 三、使用命令

```cmd
-dir string
 Nginx配置文件目录（默认为当前目录） (default ".")
-load string
 IP映射CSV文件路径（默认为template.csv） (default "template.csv")
-out string
 输出CSV文件路径（默认为results.csv） (default "results.csv")
```

### 示例命令

CMD运行

```CMD
NginxUrlScan.exe -dir .\conf\ -load template.csv -out results.csv
```

Go语言运行

```go
go run nginx_url_scan.go -dir .\conf\ -load template.csv -out results.csv
```

运行结果如下：

![](.\images\Snipaste_2025-12-22_17-14-35.png)
"# NginxUrlScan" 
