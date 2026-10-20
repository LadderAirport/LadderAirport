//go:build android || with_android

package control

import (
	stdjson "encoding/json"
	"fmt"
	"log"
)

func sanitizePlatformConfig(configJSON string) (string, error) {
	return sanitizeAndroidConfig(configJSON)
}

// sanitizeAndroidConfig 净化针对 Android 节点的配置。
// Android 普通应用缺乏 CAP_NET_RAW 与 Netlink 权限，
// 若配置了 bind_interface 或 default_interface 会导致底层拨号失败，
// 自动清除并降级为系统默认网络路由。
func sanitizeAndroidConfig(configJSON string) (string, error) {
	var document map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal([]byte(configJSON), &document); err != nil {
		return "", fmt.Errorf("解析节点配置失败：%w", err)
	}
	modified := false

	// 1. 出站 / 入站：清除 bind_interface
	for _, key := range []string{"outbounds", "inbounds"} {
		raw, ok := document[key]
		if !ok {
			continue
		}
		var entries []map[string]any
		if err := stdjson.Unmarshal(raw, &entries); err != nil {
			continue
		}
		entriesMod := false
		for i, entry := range entries {
			if bindIface, has := entry["bind_interface"]; has && bindIface != "" && bindIface != nil {
				tag, _ := entry["tag"].(string)
				log.Printf("[Android] 检测到 %s %q 配置了 bind_interface=%v，因 Android 系统权限限制已自动降级为默认网络路由", key, tag, bindIface)
				delete(entries[i], "bind_interface")
				entriesMod = true
			}
		}
		if entriesMod {
			data, err := stdjson.Marshal(entries)
			if err != nil {
				return "", fmt.Errorf("编码 %s 失败：%w", key, err)
			}
			document[key] = data
			modified = true
		}
	}

	// 2. DNS 服务器同样不能绑网卡，否则 DoH 拨号失败。
	dnsStripped, err := stripDNSServerBinds(document)
	if err != nil {
		return "", err
	}
	if dnsStripped {
		modified = true
	}

	// 3. 路由配置清理：清除 default_interface 避免触发 Netlink 接口解析错误
	if rawRoute, ok := document["route"]; ok {
		var route map[string]any
		if err := stdjson.Unmarshal(rawRoute, &route); err == nil {
			if defIface, has := route["default_interface"]; has && defIface != "" && defIface != nil {
				log.Printf("[Android] 检测到路由配置了 default_interface=%v，因 Android 系统权限限制已自动降级为系统默认网络", defIface)
				delete(route, "default_interface")
				data, err := stdjson.Marshal(route)
				if err != nil {
					return "", fmt.Errorf("编码 route 失败：%w", err)
				}
				document["route"] = data
				modified = true
			}
		}
	}

	if !modified {
		return configJSON, nil
	}
	updated, err := stdjson.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("编码更新后的节点配置失败：%w", err)
	}
	return string(updated), nil
}
