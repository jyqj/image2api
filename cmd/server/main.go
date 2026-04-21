package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/account"
	"github.com/432539/gpt2api/internal/audit"
	"github.com/432539/gpt2api/internal/backup"
	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/db"
	"github.com/432539/gpt2api/internal/gateway"
	"github.com/432539/gpt2api/internal/image"
	modelpkg "github.com/432539/gpt2api/internal/model"
	"github.com/432539/gpt2api/internal/proxy"
	"github.com/432539/gpt2api/internal/scheduler"
	"github.com/432539/gpt2api/internal/middleware"
	"github.com/432539/gpt2api/internal/server"
	"github.com/432539/gpt2api/internal/settings"
	"github.com/432539/gpt2api/internal/usage"
	"github.com/432539/gpt2api/pkg/crypto"
	"github.com/432539/gpt2api/pkg/lock"
	"github.com/432539/gpt2api/pkg/logger"
	"github.com/432539/gpt2api/pkg/mailer"
)

var (
	configPath = flag.String("c", "configs/config.yaml", "config file path")
	showVer    = flag.Bool("v", false, "show version and exit")
)

var (
	version   = "0.2.0-dev"
	buildTime = "unknown"
)

func main() {
	flag.Parse()
	if *showVer {
		fmt.Printf("gpt2api %s (build %s)\n", version, buildTime)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	if err := logger.Init(cfg.Log.Level, cfg.Log.Format, cfg.Log.Output); err != nil {
		fmt.Fprintf(os.Stderr, "init logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	log := logger.L()
	log.Info("boot gpt2api local 2api",
		zap.String("version", version),
		zap.String("env", cfg.App.Env),
		zap.String("listen", cfg.App.Listen),
	)

	sqldb, err := db.NewMySQL(cfg.MySQL)
	if err != nil {
		log.Fatal("mysql init", zap.Error(err))
	}
	defer sqldb.Close()

	rdb, err := db.NewRedis(cfg.Redis)
	if err != nil {
		log.Fatal("redis init", zap.Error(err))
	}
	defer rdb.Close()

	cipher, err := crypto.NewAESGCM(cfg.Crypto.AESKey)
	if err != nil {
		log.Fatal("crypto init", zap.Error(err))
	}
	gateway.InitImageProxySecret(cfg.Crypto.AESKey)
	middleware.InitTokenSecret(cfg.Crypto.AESKey)

	proxyDAO := proxy.NewDAO(sqldb)
	proxySvc := proxy.NewService(proxyDAO, cipher)

	accDAO := account.NewDAO(sqldb)
	accSvc := account.NewService(accDAO, cipher)

	modelDAO := modelpkg.NewDAO(sqldb)
	modelReg := modelpkg.NewRegistry(modelDAO)
	if err := modelReg.Preload(context.Background()); err != nil {
		log.Warn("model preload failed", zap.Error(err))
	}

	rl := lock.NewRedisLock(rdb)
	sched := scheduler.New(accSvc, proxySvc, rl, cfg.Scheduler)

	usageLogger := usage.New(sqldb, usage.Options{})
	defer usageLogger.Close()

	gwH := &gateway.Handler{
		Models:    modelReg,
		Scheduler: sched,
		Usage:     usageLogger,
		AccSvc:    accSvc,
	}

	imageDAO := image.NewDAO(sqldb)
	imageRunner := image.NewRunner(sched, imageDAO)
	imagesH := &gateway.ImagesHandler{
		Handler: gwH,
		Runner:  imageRunner,
		DAO:     imageDAO,
	}
	gwH.Images = imagesH

	auditDAO := audit.NewDAO(sqldb)
	auditH := audit.NewHandler(auditDAO)

	var backupH *backup.Handler
	backupDAO := backup.NewDAO(sqldb)
	if backupSvc, err := backup.New(cfg.Backup, cfg.MySQL, backupDAO); err != nil {
		log.Warn("backup service disabled", zap.Error(err))
	} else {
		backupH = backup.NewHandler(backupSvc, backupDAO, auditDAO)
		log.Info("backup service ready", zap.String("dir", backupSvc.Dir()))
	}

	adminModelH := modelpkg.NewAdminHandler(modelDAO, modelReg, auditDAO)
	usageQDAO := usage.NewQueryDAO(sqldb)
	adminUsageH := usage.NewAdminHandler(usageQDAO)
	meUsageH := usage.NewMeHandler(usageQDAO)
	meImageH := image.NewMeHandler(imageDAO)

	mailSvc := mailer.New(mailer.Config{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.Username,
		Password: cfg.SMTP.Password,
		From:     cfg.SMTP.From,
		FromName: cfg.SMTP.FromName,
		UseTLS:   cfg.SMTP.UseTLS,
	}, log)
	defer mailSvc.Close()
	if mailSvc.Disabled() {
		log.Info("mail channel disabled (smtp.host empty)")
	} else {
		log.Info("mail channel ready", zap.String("host", cfg.SMTP.Host))
	}

	settingsDAO := settings.NewDAO(sqldb)
	settingsSvc := settings.NewService(settingsDAO)
	if err := settingsSvc.Reload(context.Background()); err != nil {
		log.Warn("settings reload failed, using defaults", zap.Error(err))
	}
	settingsH := settings.NewHandler(settingsSvc, mailSvc, auditDAO)
	gwH.Settings = settingsSvc
	sched.SetRuntime(scheduler.RuntimeParams{
		Cooldown429Sec:    settingsSvc.Cooldown429Sec,
		WarnedPauseHrs:    settingsSvc.WarnedPauseHours,
		QueueWaitSec:      settingsSvc.DispatchQueueWaitSec,
		ImageExploreRatio: settingsSvc.ImageExploreRatio,
	})

	proxyH := proxy.NewHandler(proxySvc)
	prober := proxy.NewProber(proxySvc, settingsSvc, log.Named("proxy-prober"))
	proxyH.SetProber(prober)

	// 代理淘汰时自动将绑定的账号重分配到其他可用代理
	prober.SetReassignFunc(func(ctx context.Context, deadProxyID uint64) {
		ids, err := proxySvc.DAO().ListBoundAccountIDs(ctx, deadProxyID)
		if err != nil || len(ids) == 0 {
			return
		}
		for _, accID := range ids {
			if newID, err := accSvc.DAO().SwitchProxy(ctx, accID, deadProxyID); err == nil {
				log.Info("proxy retired: reassigned account",
					zap.Uint64("account_id", accID),
					zap.Uint64("old_proxy_id", deadProxyID),
					zap.Uint64("new_proxy_id", newID))
			}
		}
	})

	proberCtx, cancelProber := context.WithCancel(context.Background())
	defer cancelProber()
	go prober.Run(proberCtx)

	accountH := account.NewHandler(accSvc)
	accRefresher := account.NewRefresher(accSvc, settingsSvc, log.Named("account-refresh"))
	accQuota := account.NewQuotaProber(accSvc, settingsSvc, log.Named("account-quota"))

	acctProxyResolver := &accountProxyResolver{accSvc: accSvc, proxySvc: proxySvc}
	accRefresher.SetProxyResolver(acctProxyResolver)
	accQuota.SetProxyResolver(acctProxyResolver)

	accountH.SetRefresher(accRefresher)
	accountH.SetProber(accQuota)
	accountH.SetSettings(settingsSvc)
	accountH.SetProxyResolver(acctProxyResolver)

	imagesH.ImageAccResolver = acctProxyResolver

	accBgCtx, cancelAccBg := context.WithCancel(context.Background())
	defer cancelAccBg()
	go accRefresher.Run(accBgCtx)
	go accQuota.Run(accBgCtx)

	deps := &server.Deps{
		Config: cfg,

		ProxyH:   proxyH,
		AccountH: accountH,

		GatewayH: gwH,
		ImagesH:  imagesH,

		BackupH:  backupH,
		AuditH:   auditH,
		AuditDAO: auditDAO,

		AdminModelH: adminModelH,
		AdminUsageH: adminUsageH,

		MeUsageH: meUsageH,
		MeImageH: meImageH,

		SettingsH:   settingsH,
		SettingsSvc: settingsSvc,
	}

	r := server.New(deps)
	srv := &http.Server{
		Addr:              cfg.App.Listen,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("http server started", zap.String("addr", cfg.App.Listen))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("http listen", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("graceful shutdown", zap.Error(err))
	}
	log.Info("bye")
}

// accountProxyResolver 把账号 ID → 代理 URL 的查询串起来。
type accountProxyResolver struct {
	accSvc   *account.Service
	proxySvc *proxy.Service
}

// ProxyURLForAccount 查账号绑定的代理并解密密码,返回可直接用于 http.ProxyURL 的 URL。
func (r *accountProxyResolver) ProxyURLForAccount(ctx context.Context, accountID uint64) string {
	if r == nil || r.accSvc == nil || r.proxySvc == nil {
		return ""
	}
	b, err := r.accSvc.GetBinding(ctx, accountID)
	if err != nil || b == nil {
		return ""
	}
	p, err := r.proxySvc.Get(ctx, b.ProxyID)
	if err != nil || p == nil || !p.Enabled {
		return ""
	}
	u, err := r.proxySvc.BuildURL(p)
	if err != nil {
		return ""
	}
	return u
}

// ProxyURLByID 按 proxy_id 直接查代理 URL。
func (r *accountProxyResolver) ProxyURLByID(ctx context.Context, proxyID uint64) string {
	if r == nil || r.proxySvc == nil || proxyID == 0 {
		return ""
	}
	p, err := r.proxySvc.Get(ctx, proxyID)
	if err != nil || p == nil || !p.Enabled {
		return ""
	}
	u, err := r.proxySvc.BuildURL(p)
	if err != nil {
		return ""
	}
	return u
}

// AuthToken 给图片代理端点用:按 accountID 解出 AT / DeviceID / cookies。
func (r *accountProxyResolver) AuthToken(ctx context.Context, accountID uint64) (string, string, string, error) {
	if r == nil || r.accSvc == nil {
		return "", "", "", fmt.Errorf("account service not ready")
	}
	a, err := r.accSvc.Get(ctx, accountID)
	if err != nil {
		return "", "", "", err
	}
	at, err := r.accSvc.DecryptAuthToken(a)
	if err != nil {
		return "", "", "", err
	}
	cookies, _ := r.accSvc.DecryptCookies(ctx, accountID)
	return at, a.OAIDeviceID, cookies, nil
}

// ProxyURL 给图片代理端点用:等价于 ProxyURLForAccount。
func (r *accountProxyResolver) ProxyURL(ctx context.Context, accountID uint64) string {
	return r.ProxyURLForAccount(ctx, accountID)
}
