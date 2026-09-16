package responses

import (
	"fmt"

	"github.com/zzycxz/fairpeer/internal/netclient"
	"github.com/zzycxz/fairpeer/internal/provider"
)

// newFromConfig adapts a resolved provider.Config into the Responses client.
// It self-registers under the "responses" and "dashscope-responses" kinds via
// init() in responses.go, so the desktop kind picker and config validation see
// them as soon as this package is linked.
func newFromConfig(cfg provider.Config) (provider.Provider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("responses: base_url is required for provider %q", cfg.Name)
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("responses: model is required for provider %q", cfg.Name)
	}
	effort, _ := cfg.Extra["effort"].(string)
	mode, _ := cfg.Extra["mode"].(string)
	webSearch, _ := cfg.Extra["web_search"].(bool)
	var stateful *bool
	switch value := cfg.Extra["stateful"].(type) {
	case bool:
		stateful = &value
	case *bool:
		stateful = value
	}
	proxy, _ := cfg.Extra["proxy_spec"].(netclient.ProxySpec)
	keyEnv, _ := cfg.Extra["api_key_env"].(string)
	keySource, _ := cfg.Extra["api_key_source"].(string)
	maxOutputTokens, _ := cfg.Extra["max_output_tokens"].(int)
	requestURL, _ := cfg.Extra["request_url"].(string)
	return New(Config{
		Name:        cfg.Name,
		DisplayName: cfg.Name,
		Protocol:    "responses",
		APIKey:      cfg.APIKey,
		BaseURL:     cfg.BaseURL,
		Model:       cfg.Model,
		Effort:      effort, Mode: mode, Stateful: stateful, WebSearch: webSearch, Proxy: proxy,
		KeyEnv: keyEnv, KeySource: keySource, MaxOutputTokens: maxOutputTokens, RequestURL: requestURL,
		// Extra 原样透传：vision 等能力开关由调用方（boot/CLI）写入
		// cfg.Extra，factory 若丢弃则 New() 读不到。
		Extra: cfg.Extra,
	}), nil
}
