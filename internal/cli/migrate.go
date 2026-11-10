package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"akhflow.ai/akhflow/internal/store"

	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply database migrations",
	RunE: func(cmd *cobra.Command, args []string) error {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return fmt.Errorf("DATABASE_URL is required")
		}
		st, err := store.New(dsn)
		if err != nil {
			return err
		}
		defer st.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := st.Migrate(ctx); err != nil {
			return err
		}
		fmt.Println("migrations applied")
		return nil
	},
}
