package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseRateLimit 解析速率限制字符串，如 "10MB/s" 转换为字节每秒
func ParseRateLimit(rateStr string) (int64, error) {
	if rateStr == "" {
		return 0, nil
	}

	// 移除空格和所有可能的后缀
	rateStr = strings.TrimSpace(rateStr)
	rateStr = strings.ToUpper(rateStr)
	rateStr = strings.TrimSuffix(rateStr, "/S")
	rateStr = strings.TrimSuffix(rateStr, "/SEC")
	rateStr = strings.TrimSuffix(rateStr, "PS")
	rateStr = strings.TrimSuffix(rateStr, "BPS")

	// 支持的单位（按从大到小排序，避免部分匹配问题）
	units := map[string]int64{
		"TB": 1024 * 1024 * 1024 * 1024,
		"GB": 1024 * 1024 * 1024,
		"MB": 1024 * 1024,
		"KB": 1024,
		"B":  1,
	}

	// 查找单位和数值
	var valueStr string
	var multiplier int64

	// 尝试匹配单位
	found := false
	for u, m := range units {
		if strings.HasSuffix(rateStr, u) {
			multiplier = m
			valueStr = strings.TrimSuffix(rateStr, u)
			found = true
			break
		}
	}

	// 如果没有找到单位，假设是纯数字（字节每秒）
	if !found {
		valueStr = rateStr
		multiplier = 1
	}

	// 解析数值部分
	value, err := strconv.ParseFloat(strings.TrimSpace(valueStr), 64)
	if err != nil {
		return 0, fmt.Errorf("无效的速率限制格式: %s (无法解析数字)", rateStr)
	}

	if value < 0 {
		return 0, fmt.Errorf("速率限制不能为负数: %s", rateStr)
	}

	// 计算最终的字节每秒值
	bps := int64(value * float64(multiplier))

	// 验证结果是否合理
	if bps < 0 {
		return 0, fmt.Errorf("速率限制溢出: %s", rateStr)
	}

	return bps, nil
}
