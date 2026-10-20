package store

import (
	"encoding/json"
	"fmt"

	"github.com/ladderairport/pkg/nodedns"
)

func encodeNodeDNS(cfg *nodedns.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("节点 DNS 不能为空")
	}
	if err := cfg.Normalize(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("编码节点 DNS 失败：%w", err)
	}
	return string(raw), nil
}
