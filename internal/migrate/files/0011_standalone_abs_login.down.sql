DROP TABLE IF EXISTS abs_standalone_opt_ins;

ALTER TABLE backend_config
  DROP COLUMN IF EXISTS standalone_login_mode;
