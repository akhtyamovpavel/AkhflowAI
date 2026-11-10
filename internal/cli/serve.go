package cli

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"akhflow.ai/akhflow/internal/events"
	"akhflow.ai/akhflow/internal/llm"
	"akhflow.ai/akhflow/internal/mcp"
	"akhflow.ai/akhflow/internal/recurring"
	"akhflow.ai/akhflow/internal/server"
	"akhflow.ai/akhflow/internal/store"

	"github.com/spf13/cobra"
)

var (
	mcpHTTPAddr string
)

var serveCmd = &cobra.Command{Use: "serve", Short: "Run akhflow services"}

var serveAPICmd = &cobra.Command{
	Use:   "api",
	Short: "Run HTTP API server",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAPI()
	},
}

var serveMCPCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run MCP server (stdio by default)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMCP()
	},
}

func init() {
	serveMCPCmd.Flags().StringVar(&mcpHTTPAddr, "http", "", "serve MCP over HTTP on addr (e.g. :9090)")
	serveCmd.AddCommand(serveAPICmd, serveMCPCmd)
}

func runAPI() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return fmt.Errorf("JWT_SECRET is required")
	}
	st, err := store.New(dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := st.Ping(ctx); err != nil {
		return fmt.Errorf("db ping: %w", err)
	}
	auth := &server.Auth{Store: st, Secret: []byte(secret), TokenTTL: 24 * time.Hour}
	llmClient := llm.NewClient(os.Getenv("OLLAMA_HOST"), os.Getenv("AGENT_MODEL"))
	bus := events.NewBus()
	srv := server.New(st, auth, llmClient, bus)
	port := os.Getenv("API_PORT")
	if port == "" {
		port = "8080"
	}
	// start recurring scheduler in background
	sched := recurring.NewScheduler(st, bus)
	go sched.Run(context.Background())
	log.Printf("akhflow api on :%s", port)
	return http.ListenAndServe(":"+port, srv.Handler())
}

func runMCP() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	st, err := store.New(dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	userID := int64(1)
	if v := os.Getenv("MCP_USER_ID"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &userID); err != nil {
			return fmt.Errorf("bad MCP_USER_ID: %w", err)
		}
	}
	actor := os.Getenv("MCP_USER_EMAIL")
	if actor == "" {
		u, err := st.UserByID(context.Background(), userID)
		if err != nil {
			return err
		}
		if u != nil {
			actor = u.Email
		}
	}
	srv := &mcp.Server{Store: st, UserID: userID, Actor: actor, Version: "0.1.0"}
	if mcpHTTPAddr != "" {
		apiKey := os.Getenv("MCP_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("MCP_API_KEY is required for MCP over HTTP")
		}
		srv.APIKey = apiKey
		log.Printf("akhflow mcp on http://%s/mcp", mcpHTTPAddr)
		mux := http.NewServeMux()
		mux.Handle("/mcp", srv.HTTPHandler())
		return http.ListenAndServe(mcpHTTPAddr, mux)
	}
	return srv.ServeStdio()
}
