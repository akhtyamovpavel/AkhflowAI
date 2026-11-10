package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	apiURL string
	token  string
)

var rootCmd = &cobra.Command{
	Use:   "akhflow",
	Short: "Akhflow-AI: AI-native task tracker",
}

func Execute() error {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&apiURL, "api", "", "API base URL (env AKHFLOW_API)")
	rootCmd.PersistentFlags().StringVar(&token, "token", "", "API token (env AKHFLOW_TOKEN)")
	rootCmd.AddCommand(serveCmd, migrateCmd, taskCmd, agentCmd)
	return rootCmd.Execute()
}

func initConfig() {
	loadDotEnv(".env")
	if apiURL == "" {
		apiURL = os.Getenv("AKHFLOW_API")
	}
	if apiURL == "" {
		apiURL = "http://localhost:8080"
	}
	if token == "" {
		token = os.Getenv("AKHFLOW_TOKEN")
	}
}

func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		val = strings.Trim(val, `"'`)
		if _, ok := os.LookupEnv(key); !ok {
			os.Setenv(key, val)
		}
	}
}
