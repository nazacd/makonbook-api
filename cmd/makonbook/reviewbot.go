package main

import (
	"context"
	"log/slog"

	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/platform/telegram"
	"github.com/nazacd/makonbook-api/internal/reviewbot"
)

// reviewBot runs the teachers' Telegram group review until stopped.
func reviewBot(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if cfg.TelegramReviewToken == "" { // not set up yet: wait, rather than restart over and over
		log.Warn("TELEGRAM_REVIEW_TOKEN not set: the review bot is off")
		<-ctx.Done()
		return nil
	}
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc, err := picturingService(cfg, pool)
	if err != nil {
		return err
	}
	bot := reviewbot.New(pool, telegram.New(cfg.TelegramReviewToken), svc, cfg.TelegramReviewChatID, cfg.Location, log)
	return bot.Run(ctx)
}
