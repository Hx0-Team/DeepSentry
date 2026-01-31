package config 

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// GlobalConfig 全局配置实例，供其他模块读取
var GlobalConfig Config

// Config 结构体定义
type Config struct {
	// --- LLM 配置 ---
	ApiURL      string  `mapstructure:"api_url"`
	ModelName   string  `mapstructure:"model_name"`
	ApiKey      string  `mapstructure:"api_key"`
	Temperature float64 `mapstructure:"temperature"`

	// --- SSH 配置 ---
	SSHHost     string `mapstructure:"ssh_host"`
	SSHUser     string `mapstructure:"ssh_user"`
	SSHPassword string `mapstructure:"ssh_password"`
	SSHKeyPath  string `mapstructure:"ssh_key_path"`
}

// InitConfig 初始化配置 (核心加载逻辑)
func InitConfig(cfgFile string) error {
	if cfgFile != "" {
		// 1. 如果用户通过命令行指定了文件，直接使用
		viper.SetConfigFile(cfgFile)
	} else {
		// 2. 否则按顺序搜索默认路径
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		// 搜索路径优先级：
		// 1. 当前目录 (.)
		viper.AddConfigPath(".")
		// 2. 用户主目录下的 .deepsentry 文件夹
		viper.AddConfigPath(filepath.Join(home, ".deepsentry"))
		// 3. 系统级配置 /etc/deepsentry
		viper.AddConfigPath("/etc/deepsentry")

		viper.SetConfigName("config") // 查找 config.yaml, config.json 等
		viper.SetConfigType("yaml")   // 默认以 yaml 格式解析
	}

	// 3. 设置默认值 (防止空配置报错)
	viper.SetDefault("api_url", "https://api.deepseek.com/chat/completions")
	viper.SetDefault("model_name", "deepseek-chat")
	viper.SetDefault("temperature", 0.0)
	viper.SetDefault("ssh_user", "root")

	// 4. 开启环境变量自动覆盖
	// 例如: export DEEPSENTRY_API_KEY="xxx" 会自动覆盖配置文件中的 api_key
	viper.SetEnvPrefix("DEEPSENTRY")
	viper.AutomaticEnv()

	// 5. 读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		// 如果只是没找到文件，返回特定错误类型，以便 main.go 决定是否进入向导
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			return err
		}
		// 其他错误（如 YAML 格式错误）直接返回
		return fmt.Errorf("配置文件读取错误: %w", err)
	}

	// 6. 将读取到的配置映射到结构体
	err := viper.Unmarshal(&GlobalConfig)
	if err != nil {
		return fmt.Errorf("配置解析失败: %w", err)
	}

	return nil
}

// SaveConfig 将当前 Viper 中的配置保存到文件 (默认保存到当前目录)
func SaveConfig() error {
	// 确保默认保存为 yaml 格式
	viper.SetConfigType("yaml")
	// 保存到当前目录下的 config.yaml
	return viper.WriteConfigAs("config.yaml")
}
