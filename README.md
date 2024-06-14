# updateProxyIP

自动从 https://zip.baipiao.eu.org 获取中转IP，取ping值最低的ip绑定域名。

## 使用方法

配置config.json

{"email": "账户email", "key": "账户key", "domainInfos":[["名称", "根域", "目标文件名关键字"], ["名称", "根域"]]}

比如完整域名为 A.B.com，根域为 B.com 则config.json 为 

{"email": "账户email", "key": "账户key", "domainInfos":[["A", "B.com"]]},

由于 https://zip.baipiao.eu.org 中提供了多条线路，多个端口，若限制使用443端口且支持tls，则 config.json 为 

{"email": "账户email", "key": "账户key", "domainInfos":[["A", "B.com", "*-1-443"]}

\*为任意匹配，\*-1-443 表示：任意线路，支持tls，443端口，123-*-8080 表示：123线路，是否tls支持均可，8080端口

具体文件名参考 https://zip.baipiao.eu.org
