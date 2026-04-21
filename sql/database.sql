CREATE DATABASE IF NOT EXISTS `gpt2api` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE `gpt2api`;

CREATE TABLE IF NOT EXISTS `proxies` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `scheme` VARCHAR(16) NOT NULL DEFAULT 'http',
  `host` VARCHAR(255) NOT NULL,
  `port` INT NOT NULL,
  `username` VARCHAR(255) NOT NULL DEFAULT '',
  `password_enc` TEXT NULL,
  `country` VARCHAR(64) NOT NULL DEFAULT '',
  `isp` VARCHAR(128) NOT NULL DEFAULT '',
  `health_score` INT NOT NULL DEFAULT 100,
  `last_probe_at` DATETIME NULL,
  `last_error` VARCHAR(512) NOT NULL DEFAULT '',
  `enabled` TINYINT(1) NOT NULL DEFAULT 1,
  `remark` VARCHAR(255) NOT NULL DEFAULT '',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` DATETIME NULL,
  PRIMARY KEY (`id`),
  KEY `idx_proxies_enabled` (`enabled`,`deleted_at`),
  UNIQUE KEY `uk_proxy_endpoint` (`scheme`,`host`,`port`,`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `oai_accounts` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `email` VARCHAR(255) NOT NULL,
  `auth_token_enc` TEXT NOT NULL,
  `refresh_token_enc` TEXT NULL,
  `session_token_enc` TEXT NULL,
  `token_expires_at` DATETIME NULL,
  `oai_session_id` VARCHAR(128) NOT NULL DEFAULT '',
  `oai_device_id` VARCHAR(128) NOT NULL DEFAULT '',
  `client_id` VARCHAR(128) NOT NULL DEFAULT '',
  `chatgpt_account_id` VARCHAR(128) NOT NULL DEFAULT '',
  `account_type` VARCHAR(64) NOT NULL DEFAULT 'codex',
  `subscription_type` VARCHAR(64) NOT NULL DEFAULT '',
  `daily_image_quota` INT NOT NULL DEFAULT 100,
  `status` VARCHAR(32) NOT NULL DEFAULT 'healthy',
  `warned_at` DATETIME NULL,
  `cooldown_until` DATETIME NULL,
  `last_used_at` DATETIME NULL,
  `today_used_count` INT NOT NULL DEFAULT 0,
  `today_used_date` DATETIME NULL,
  `last_refresh_at` DATETIME NULL,
  `last_refresh_source` VARCHAR(32) NOT NULL DEFAULT '',
  `refresh_error` VARCHAR(512) NOT NULL DEFAULT '',
  `image_quota_remaining` INT NOT NULL DEFAULT -1,
  `image_quota_total` INT NOT NULL DEFAULT -1,
  `image_quota_reset_at` DATETIME NULL,
  `image_quota_updated_at` DATETIME NULL,
  `image_capability_status` VARCHAR(32) NOT NULL DEFAULT 'unknown',
  `image_capability_model` VARCHAR(128) NOT NULL DEFAULT '',
  `image_capability_source` VARCHAR(32) NOT NULL DEFAULT '',
  `image_capability_detail` TEXT NULL,
  `image_capability_updated_at` DATETIME NULL,
  `image_init_blocked_features` TEXT NULL,
  `img2_hit_count` INT NOT NULL DEFAULT 0,
  `img2_preview_only_count` INT NOT NULL DEFAULT 0,
  `img2_miss_count` INT NOT NULL DEFAULT 0,
  `img2_consecutive_miss` INT NOT NULL DEFAULT 0,
  `img2_last_status` VARCHAR(32) NOT NULL DEFAULT '',
  `img2_last_hit_at` DATETIME NULL,
  `img2_last_attempt_at` DATETIME NULL,
  `img2_delivery_success_count` INT NOT NULL DEFAULT 0,
  `img2_delivery_fail_count` INT NOT NULL DEFAULT 0,
  `img2_delivery_partial_count` INT NOT NULL DEFAULT 0,
  `img2_last_delivery_status` VARCHAR(32) NOT NULL DEFAULT '',
  `img2_last_delivery_at` DATETIME NULL,
  `notes` TEXT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` DATETIME NULL,
  PRIMARY KEY (`id`),
  KEY `idx_oai_accounts_status` (`status`,`deleted_at`),
  KEY `idx_oai_accounts_email` (`email`),
  KEY `idx_oai_accounts_refresh` (`token_expires_at`,`deleted_at`),
  KEY `idx_oai_accounts_quota` (`image_quota_updated_at`,`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `oai_account_cookies` (
  `account_id` BIGINT UNSIGNED NOT NULL,
  `cookie_json_enc` MEDIUMTEXT NOT NULL,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`account_id`),
  CONSTRAINT `fk_oai_account_cookies_account` FOREIGN KEY (`account_id`) REFERENCES `oai_accounts` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `account_proxy_bindings` (
  `account_id` BIGINT UNSIGNED NOT NULL,
  `proxy_id` BIGINT UNSIGNED NOT NULL,
  `bound_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`account_id`),
  KEY `idx_account_proxy_bindings_proxy` (`proxy_id`),
  CONSTRAINT `fk_apb_account` FOREIGN KEY (`account_id`) REFERENCES `oai_accounts` (`id`) ON DELETE CASCADE,
  CONSTRAINT `fk_apb_proxy` FOREIGN KEY (`proxy_id`) REFERENCES `proxies` (`id`) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `models` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `slug` VARCHAR(64) NOT NULL,
  `type` VARCHAR(16) NOT NULL,
  `upstream_model_slug` VARCHAR(128) NOT NULL,
  `description` VARCHAR(255) NOT NULL DEFAULT '',
  `enabled` TINYINT(1) NOT NULL DEFAULT 1,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` DATETIME NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_models_slug` (`slug`),
  KEY `idx_models_enabled` (`enabled`,`deleted_at`),
  KEY `idx_models_type` (`type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `models` (`slug`,`type`,`upstream_model_slug`,`description`,`enabled`) VALUES
  ('gpt-4o','chat','auto','兼容旧客户端的对话模型映射',0),
  ('gpt-image-2','image','auto','ChatGPT 图像生成',1)
ON DUPLICATE KEY UPDATE
  `type`=VALUES(`type`),
  `upstream_model_slug`=VALUES(`upstream_model_slug`),
  `description`=VALUES(`description`),
  `enabled`=VALUES(`enabled`);

CREATE TABLE IF NOT EXISTS `usage_logs` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `model_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `account_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `request_id` VARCHAR(64) NOT NULL DEFAULT '',
  `type` VARCHAR(16) NOT NULL,
  `input_tokens` INT NOT NULL DEFAULT 0,
  `output_tokens` INT NOT NULL DEFAULT 0,
  `cache_read_tokens` INT NOT NULL DEFAULT 0,
  `cache_write_tokens` INT NOT NULL DEFAULT 0,
  `image_count` INT NOT NULL DEFAULT 0,
  `duration_ms` INT NOT NULL DEFAULT 0,
  `status` VARCHAR(16) NOT NULL DEFAULT '',
  `error_code` VARCHAR(128) NOT NULL DEFAULT '',
  `ip` VARCHAR(64) NOT NULL DEFAULT '',
  `ua` VARCHAR(255) NOT NULL DEFAULT '',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_usage_logs_created` (`created_at`),
  KEY `idx_usage_logs_model` (`model_id`),
  KEY `idx_usage_logs_account` (`account_id`),
  KEY `idx_usage_logs_type_status` (`type`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `image_tasks` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `task_id` VARCHAR(64) NOT NULL,
  `model_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `account_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `prompt` TEXT NOT NULL,
  `n` INT NOT NULL DEFAULT 1,
  `size` VARCHAR(32) NOT NULL DEFAULT '1024x1024',
  `status` VARCHAR(32) NOT NULL DEFAULT 'queued',
  `conversation_id` VARCHAR(128) NOT NULL DEFAULT '',
  `file_ids` JSON NULL,
  `result_urls` JSON NULL,
  `error` VARCHAR(512) NOT NULL DEFAULT '',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `started_at` DATETIME NULL,
  `finished_at` DATETIME NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_image_tasks_task_id` (`task_id`),
  KEY `idx_image_tasks_status` (`status`),
  KEY `idx_image_tasks_created` (`created_at`),
  KEY `idx_image_tasks_account` (`account_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system_settings` (
  `k` VARCHAR(128) NOT NULL,
  `v` TEXT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`k`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `system_settings` (`k`,`v`) VALUES
  ('site.name','GPT2API Local'),
  ('site.description','自用 OpenAI 兼容 2API 中转'),
  ('site.logo_url',''),
  ('site.footer',''),
  ('site.contact_email',''),
  ('site.docs_url',''),
  ('site.api_base_url',''),
  ('ui.default_page_size','20'),
  ('gateway.upstream_timeout_sec','60'),
  ('gateway.sse_read_timeout_sec','120'),
  ('gateway.cooldown_429_sec','300'),
  ('gateway.warned_pause_hours','24'),
  ('gateway.daily_usage_ratio','0.8'),
  ('gateway.retry_on_failure','true'),
  ('gateway.retry_max','1'),
  ('gateway.dispatch_queue_wait_sec','120'),
  ('gateway.image_explore_ratio','0.2'),
  ('proxy.probe_enabled','true'),
  ('proxy.probe_interval_sec','300'),
  ('proxy.probe_timeout_sec','10'),
  ('proxy.probe_target_url','https://chatgpt.com/cdn-cgi/trace'),
  ('proxy.probe_concurrency','8'),
  ('account.refresh_enabled','true'),
  ('account.refresh_interval_sec','120'),
  ('account.refresh_ahead_sec','900'),
  ('account.refresh_concurrency','4'),
  ('account.quota_probe_enabled','true'),
  ('account.quota_probe_interval_sec','900'),
  ('account.default_client_id','app_LlGpXReQgckcGGUo2JrYvtJK'),
  ('mail.enabled_display','auto')
ON DUPLICATE KEY UPDATE `v`=VALUES(`v`);

CREATE TABLE IF NOT EXISTS `admin_audit_logs` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `actor_id` BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `actor_email` VARCHAR(255) NOT NULL DEFAULT '',
  `action` VARCHAR(128) NOT NULL DEFAULT '',
  `method` VARCHAR(16) NOT NULL DEFAULT '',
  `path` VARCHAR(255) NOT NULL DEFAULT '',
  `status_code` INT NOT NULL DEFAULT 0,
  `ip` VARCHAR(64) NOT NULL DEFAULT '',
  `ua` VARCHAR(255) NOT NULL DEFAULT '',
  `target` VARCHAR(128) NOT NULL DEFAULT '',
  `meta` JSON NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_admin_audit_created` (`created_at`),
  KEY `idx_admin_audit_action` (`action`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `backup_files` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `backup_id` VARCHAR(64) NOT NULL,
  `file_name` VARCHAR(255) NOT NULL,
  `size_bytes` BIGINT NOT NULL DEFAULT 0,
  `sha256` VARCHAR(64) NOT NULL DEFAULT '',
  `trigger` VARCHAR(32) NOT NULL DEFAULT '',
  `status` VARCHAR(32) NOT NULL DEFAULT 'running',
  `error` VARCHAR(512) NOT NULL DEFAULT '',
  `include_data` TINYINT(1) NOT NULL DEFAULT 1,
  `created_by` BIGINT UNSIGNED NOT NULL DEFAULT 0,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `finished_at` DATETIME NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_backup_files_backup_id` (`backup_id`),
  KEY `idx_backup_files_created` (`created_at`),
  KEY `idx_backup_files_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
