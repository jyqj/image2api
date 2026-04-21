package backup

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	mysqlDrv "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/pkg/logger"
)

// ErrRestoreDisabled 恢复功能被配置禁用。
var ErrRestoreDisabled = errors.New("backup: restore is disabled by config")

// ErrInvalidFileName 非法文件名(防路径遍历)。
var ErrInvalidFileName = errors.New("backup: invalid file name")

// safeNameRe 仅允许字母/数字/点/下划线/连字符。
var safeNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+\.sql(\.gz)?$`)

// backupIDRe 形如 bk_20260417_120000_xxxx
var backupIDRe = regexp.MustCompile(`^bk_\d{8}_\d{6}_[a-z0-9]{6}$`)

// Service 备份服务。
type Service struct {
	cfg    config.BackupConfig
	mysql  config.MySQLConfig
	dao    *DAO
	dbName string
	dsn    *mysqlDrv.Config

	// 启动时探测 mysqldump 支持的参数;不支持时自动降级。
	// 例如 mariadb-dump 不识别 --set-gtid-purged、--column-statistics 等 MySQL 专属 flag。
	supportSetGTIDPurged bool
	supportColumnStats   bool
	isMariaDB            bool
}

// New 构造 Service。cfg.Dir 不存在时自动创建。
func New(cfg config.BackupConfig, mysqlCfg config.MySQLConfig, dao *DAO) (*Service, error) {
	if cfg.Dir == "" {
		cfg.Dir = "./data/backups"
	}
	if cfg.MysqldumpBin == "" {
		cfg.MysqldumpBin = "mysqldump"
	}
	if cfg.MysqlBin == "" {
		cfg.MysqlBin = "mysql"
	}
	if cfg.MaxUploadMB <= 0 {
		cfg.MaxUploadMB = 512
	}

	dsn, err := mysqlDrv.ParseDSN(mysqlCfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse mysql dsn: %w", err)
	}
	if dsn.DBName == "" {
		return nil, errors.New("backup: mysql dsn missing db name")
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}
	svc := &Service{
		cfg:    cfg,
		mysql:  mysqlCfg,
		dao:    dao,
		dbName: dsn.DBName,
		dsn:    dsn,
	}
	svc.probeMysqldump()
	return svc, nil
}

// probeMysqldump 探测 mysqldump 版本 / 是否为 MariaDB,以决定传哪些参数。
// 失败时不 panic,只是使用保守默认(不加这些 flag)。
func (s *Service) probeMysqldump() {
	out, err := exec.Command(s.cfg.MysqldumpBin, "--version").CombinedOutput()
	if err != nil {
		logger.L().Warn("probe mysqldump failed, fallback to conservative args",
			zap.Error(err))
		return
	}
	ver := strings.ToLower(string(out))
	s.isMariaDB = strings.Contains(ver, "mariadb")
	// MySQL 5.6+ 支持 --set-gtid-purged;MariaDB 和极老的 MySQL 不支持
	s.supportSetGTIDPurged = !s.isMariaDB
	// --column-statistics 仅 MySQL 8.0+ 的 mysqldump 支持
	s.supportColumnStats = !s.isMariaDB && strings.Contains(ver, "distrib 8.")
	logger.L().Info("mysqldump probed",
		zap.Bool("mariadb", s.isMariaDB),
		zap.Bool("gtid_flag", s.supportSetGTIDPurged),
		zap.String("version_line", firstLine(string(out))),
	)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// Dir 返回备份目录(只读)。
func (s *Service) Dir() string { return s.cfg.Dir }

// MaxUploadBytes 返回上传上限(bytes)。
func (s *Service) MaxUploadBytes() int64 {
	return int64(s.cfg.MaxUploadMB) * 1024 * 1024
}

// AllowRestore 是否允许 /restore 端点。
func (s *Service) AllowRestore() bool { return s.cfg.AllowRestore }

// Create 同步执行一次 mysqldump 备份。成功返回 File 记录。
// actorID 是本地操作者标记(默认 0);includeData=false 时仅 dump 表结构。
//
// 流程:记录插入 running → 执行 mysqldump | gzip → sha256 → MarkReady。
func (s *Service) Create(ctx context.Context, actorID uint64, trigger string, includeData bool) (*File, error) {
	backupID := generateBackupID()
	fileName := backupID + ".sql.gz"
	fullPath := filepath.Join(s.cfg.Dir, fileName)

	f := &File{
		BackupID:    backupID,
		FileName:    fileName,
		Trigger:     trigger,
		Status:      StatusRunning,
		IncludeData: includeData,
		CreatedBy:   actorID,
	}
	if err := s.dao.Create(ctx, f); err != nil {
		return nil, err
	}

	size, sha, err := s.dumpToFile(ctx, fullPath, includeData)
	if err != nil {
		_ = s.dao.MarkFailed(ctx, backupID, err.Error())
		_ = os.Remove(fullPath)
		return nil, err
	}
	if err := s.dao.MarkReady(ctx, backupID, size, sha); err != nil {
		return nil, err
	}
	f.SizeBytes = size
	f.SHA256 = sha
	f.Status = StatusReady
	// 异步做 retention,不阻塞
	if s.cfg.Retention > 0 {
		go s.runRetention()
	}
	return f, nil
}

// dumpToFile 实际执行 mysqldump → gzip → 落盘 + sha256。
func (s *Service) dumpToFile(ctx context.Context, fullPath string, includeData bool) (int64, string, error) {
	out, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, "", fmt.Errorf("open backup file: %w", err)
	}
	defer out.Close()

	sha := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(out, sha))
	defer gz.Close()

	args := s.mysqldumpArgs(includeData)
	cmd := exec.CommandContext(ctx, s.cfg.MysqldumpBin, args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+s.dsn.Passwd)

	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, "", fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return 0, "", fmt.Errorf("start mysqldump: %w (stderr=%s)", err, stderr.String())
	}

	written, err := io.Copy(gz, stdout)
	if copyErr := gz.Close(); copyErr != nil && err == nil {
		err = copyErr
	}
	if werr := cmd.Wait(); werr != nil {
		stderrTail := stderr.String()
		if len(stderrTail) > 800 {
			stderrTail = stderrTail[len(stderrTail)-800:]
		}
		return 0, "", fmt.Errorf("mysqldump failed: %w (stderr=%s)", werr, stderrTail)
	}
	if err != nil {
		return 0, "", err
	}
	if err := out.Sync(); err != nil {
		return 0, "", err
	}

	info, err := out.Stat()
	if err != nil {
		return 0, "", err
	}
	_ = written // compressed write counter is inside gz, 直接用落盘大小
	return info.Size(), hex.EncodeToString(sha.Sum(nil)), nil
}

// mysqldumpArgs 生成命令行参数。
// 注意:密码通过环境变量 MYSQL_PWD 传,不暴露在命令行。
func (s *Service) mysqldumpArgs(includeData bool) []string {
	host, port := s.dsnHostPort()
	args := []string{
		"-h", host,
		"-P", port,
		"-u", s.dsn.User,
		"--default-character-set=utf8mb4",
		"--single-transaction",
		"--quick",
		"--skip-lock-tables",
		"--hex-blob",
		"--routines",
		"--triggers",
		"--events",
	}
	if s.supportSetGTIDPurged {
		args = append(args, "--set-gtid-purged=OFF")
	}
	if s.supportColumnStats {
		// MySQL 8 默认会查 column_statistics,对目标库可能没权限,强制关掉
		args = append(args, "--column-statistics=0")
	}
	if !includeData {
		args = append(args, "--no-data")
	}
	// 只 dump 业务库;排除审计表避免恢复时覆盖当下审计
	args = append(args, "--ignore-table="+s.dbName+".admin_audit_logs")
	args = append(args, s.dbName)
	return args
}

// Restore 同步恢复一个已存在的备份到 MySQL。
// 调用方必须保证传入的是可信来源,并在操作前后写入审计。
func (s *Service) Restore(ctx context.Context, backupID string) error {
	if !s.cfg.AllowRestore {
		return ErrRestoreDisabled
	}
	f, err := s.dao.Get(ctx, backupID)
	if err != nil {
		return err
	}
	if f.Status != StatusReady {
		return fmt.Errorf("backup not ready: %s", f.Status)
	}
	fullPath := filepath.Join(s.cfg.Dir, f.FileName)
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(s.cfg.Dir)+string(filepath.Separator)) {
		return ErrInvalidFileName
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return fmt.Errorf("open backup file: %w", err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	host, port := s.dsnHostPort()
	args := []string{
		"-h", host,
		"-P", port,
		"-u", s.dsn.User,
		"--default-character-set=utf8mb4",
		s.dbName,
	}
	cmd := exec.CommandContext(ctx, s.cfg.MysqlBin, args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+s.dsn.Passwd)
	cmd.Stdin = gz
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		tail := stderr.String()
		if len(tail) > 800 {
			tail = tail[len(tail)-800:]
		}
		return fmt.Errorf("mysql restore failed: %w (stderr=%s)", err, tail)
	}
	return nil
}

// Delete 删除备份记录 + 物理文件。
func (s *Service) Delete(ctx context.Context, backupID string) error {
	if !backupIDRe.MatchString(backupID) {
		return ErrInvalidFileName
	}
	f, err := s.dao.Get(ctx, backupID)
	if err != nil {
		return err
	}
	path, err := s.fullPath(f.FileName)
	if err != nil {
		return err
	}
	_ = os.Remove(path) // 即便文件丢失也要清理 DB
	return s.dao.Delete(ctx, backupID)
}

// OpenForDownload 返回只读 handle,调用方负责 Close。
// 额外返回文件名和大小供 HTTP header 使用。
func (s *Service) OpenForDownload(ctx context.Context, backupID string) (*os.File, *File, error) {
	f, err := s.dao.Get(ctx, backupID)
	if err != nil {
		return nil, nil, err
	}
	if f.Status != StatusReady {
		return nil, nil, fmt.Errorf("backup not ready: %s", f.Status)
	}
	path, err := s.fullPath(f.FileName)
	if err != nil {
		return nil, nil, err
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return fh, f, nil
}

// ImportUpload 将上传的 .sql.gz 保存到备份目录并登记为 "upload" trigger。
// 传入的 reader 会被逐字节读入(已做 LimitReader 控制)。
func (s *Service) ImportUpload(ctx context.Context, actorID uint64, origName string, src io.Reader) (*File, error) {
	if origName == "" {
		origName = "upload.sql.gz"
	}
	// 只取 basename,防路径遍历
	origName = filepath.Base(origName)
	if !safeNameRe.MatchString(origName) {
		return nil, ErrInvalidFileName
	}
	if !strings.HasSuffix(origName, ".gz") {
		// 要求 .sql.gz。纯 .sql 也接受,但我们要 gzip 之后再存。
		// 这里简单起见,拒绝非 .gz(避免引入额外的 gzip 临时流)。
		return nil, fmt.Errorf("upload must be gzip-compressed (.sql.gz)")
	}
	backupID := generateBackupID()
	fileName := backupID + ".sql.gz"
	fullPath := filepath.Join(s.cfg.Dir, fileName)

	out, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		return nil, fmt.Errorf("create upload file: %w", err)
	}
	defer out.Close()

	sha := sha256.New()
	limited := io.LimitReader(src, s.MaxUploadBytes()+1)
	n, err := io.Copy(io.MultiWriter(out, sha), limited)
	if err != nil {
		_ = os.Remove(fullPath)
		return nil, fmt.Errorf("write upload: %w", err)
	}
	if n > s.MaxUploadBytes() {
		_ = os.Remove(fullPath)
		return nil, fmt.Errorf("upload exceeds max %d MB", s.cfg.MaxUploadMB)
	}
	// gzip 合法性快速校验(读 header)
	if err := verifyGzipHeader(fullPath); err != nil {
		_ = os.Remove(fullPath)
		return nil, fmt.Errorf("invalid gzip: %w", err)
	}

	f := &File{
		BackupID:    backupID,
		FileName:    fileName,
		SizeBytes:   n,
		SHA256:      hex.EncodeToString(sha.Sum(nil)),
		Trigger:     TriggerUpload,
		Status:      StatusReady,
		IncludeData: true,
		CreatedBy:   actorID,
	}
	if err := s.dao.Create(ctx, f); err != nil {
		_ = os.Remove(fullPath)
		return nil, err
	}
	// Create 是插入 running,立刻补 ready
	if err := s.dao.MarkReady(ctx, backupID, n, f.SHA256); err != nil {
		logger.L().Warn("mark upload ready", zap.Error(err))
	}
	f.Status = StatusReady
	return f, nil
}

// runRetention 清理超过 cfg.Retention 的旧备份。只对 ready 状态生效。
func (s *Service) runRetention() {
	if s.cfg.Retention <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	old, err := s.dao.ListReadyOldest(ctx, s.cfg.Retention)
	if err != nil {
		logger.L().Warn("retention list failed", zap.Error(err))
		return
	}
	for _, f := range old {
		path, err := s.fullPath(f.FileName)
		if err == nil {
			_ = os.Remove(path)
		}
		_ = s.dao.Delete(ctx, f.BackupID)
		logger.L().Info("backup pruned by retention", zap.String("id", f.BackupID))
	}
}

func (s *Service) fullPath(fileName string) (string, error) {
	if !safeNameRe.MatchString(fileName) {
		return "", ErrInvalidFileName
	}
	p := filepath.Join(s.cfg.Dir, fileName)
	// 严格校验:清洗后的路径必须仍在 backup dir 下
	cleanDir := filepath.Clean(s.cfg.Dir)
	cleanPath := filepath.Clean(p)
	if !strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator)) && cleanPath != cleanDir {
		return "", ErrInvalidFileName
	}
	return p, nil
}

func (s *Service) dsnHostPort() (string, string) {
	host, port := "127.0.0.1", "3306"
	addr := s.dsn.Addr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		host = addr[:i]
		port = addr[i+1:]
	} else if addr != "" {
		host = addr
	}
	return host, port
}

// generateBackupID 生成形如 bk_20260417_120000_xyz987 的唯一 id。
func generateBackupID() string {
	now := time.Now()
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	//nolint:gosec
	rng := rand.New(rand.NewSource(now.UnixNano()))
	suffix := make([]byte, 6)
	for i := range suffix {
		suffix[i] = letters[rng.Intn(len(letters))]
	}
	return fmt.Sprintf("bk_%s_%s", now.Format("20060102_150405"), string(suffix))
}

// verifyGzipHeader 快速校验文件是合法 gzip。
func verifyGzipHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	_ = gz.Close()
	return nil
}
