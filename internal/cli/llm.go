package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zuhailkhan/threadman/internal/llm"
)

func newLLMCmd(cfgPath string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "llm",
		Short: "Configure the language model used to summarize threads on handoff",
	}
	cmd.AddCommand(newLLMSetCmd(cfgPath), newLLMShowCmd(cfgPath), newLLMTestCmd(cfgPath), newLLMClearCmd(cfgPath))
	return cmd
}

func newLLMSetCmd(cfgPath string) *cobra.Command {
	var cfg llm.Config

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Choose the provider and model",
		Example: `  threadman llm set --provider ollama --model llama3.2
  threadman llm set --provider lmstudio --model qwen2.5-coder-14b
  threadman llm set --provider claude
  threadman llm set --provider openai --model <model-name>
  threadman llm set --provider deepseek`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			saved, err := llm.Save(cfgPath, cfg)
			if err != nil {
				return err
			}
			fmt.Printf("saved: %s\n", saved.Label())
			fmt.Printf("api key: %s\n", saved.KeySource())
			fmt.Println("run 'threadman llm test' to check the connection")
			return nil
		},
	}

	cmd.Flags().StringVar(&cfg.Provider, "provider", "", "one of: "+strings.Join(llm.Providers(), ", "))
	cmd.Flags().StringVar(&cfg.Model, "model", "", "model name (optional for claude and deepseek)")
	cmd.Flags().StringVar(&cfg.BaseURL, "base-url", "", "override the provider's API address")
	cmd.Flags().StringVar(&cfg.APIKey, "api-key", "", "store an API key in the system keyring; omit to use the provider's environment variable")
	_ = cmd.MarkFlagRequired("provider")
	return cmd
}

func newLLMShowCmd(cfgPath string) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the configured model",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok, err := llm.Load(cfgPath)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("no LLM configured; handoff offers the full transcript only")
				fmt.Println("run 'threadman llm set --provider ... --model ...'")
				return nil
			}
			fmt.Printf("provider: %s\n", cfg.Provider)
			fmt.Printf("model:    %s\n", cfg.Model)
			if cfg.BaseURL != "" {
				fmt.Printf("base url: %s\n", cfg.BaseURL)
			}
			fmt.Printf("api key:  %s\n", cfg.KeySource())
			fmt.Printf("config:   %s\n", cfgPath)
			return nil
		},
	}
}

func newLLMTestCmd(cfgPath string) *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Check that the configured model is reachable",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok, err := llm.Load(cfgPath)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no LLM configured; run 'threadman llm set'")
			}
			client, err := llm.New(cfg)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := client.Ping(ctx); err != nil {
				return fmt.Errorf("%s: %w", cfg.Label(), err)
			}
			fmt.Printf("ok: %s\n", cfg.Label())
			return nil
		},
	}
}

func newLLMClearCmd(cfgPath string) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove the LLM configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := llm.Clear(cfgPath); err != nil {
				return err
			}
			fmt.Println("LLM configuration removed")
			return nil
		},
	}
}
