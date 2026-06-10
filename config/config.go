package config

import (
	"os"
)

type Config struct {
	Port              string
	ModelList         map[string]bool
	JWTSecret         string
	SessionMgrURL     string
	WorkflowURL       string
	C2DashboardURL    string
	A1AccountURL      string
	SkillRegistryURL  string
	AIModelURL        string
	StoppedTasksFile  string
	FanqieScript      string
	A1BaseURL         string
	DB_DSN            string
}

func Load() *Config {
	return &Config{
		Port:            getEnv("PORT", "8080"),
		JWTSecret:       getEnv("JWT_SECRET", "dev-secret-change-in-production"),
		SessionMgrURL:   getEnv("SESSION_MGR_URL", "http://localhost:18080"),
		WorkflowURL:     getEnv("WORKFLOW_URL", "http://localhost:9100"),
		C2DashboardURL:  getEnv("C2_DASHBOARD_URL", "http://localhost:8083"),
		A1AccountURL:    getEnv("A1_ACCOUNT_URL", "http://localhost:8084"),
		SkillRegistryURL: getEnv("SKILL_REGISTRY_URL", "http://localhost:18090"),
		AIModelURL:       getEnv("AI_MODEL_URL", "http://localhost:18180"),
		StoppedTasksFile:  getEnv("STOPPED_TASKS_FILE", "/tmp/sm_demo/stopped_tasks.json"),
		FanqieScript:      getEnv("FANQIE_SCRIPT", "/home/claw_studios/code/L1_AI_Releaser/scripts/publish_fanqie.js"),
		A1BaseURL:         getEnv("A1_BASE_URL", "http://localhost:8084"),
		DB_DSN:            getEnv("DB_DSN", "user:password@tcp(127.0.0.1:3306)/claw_studios?parseTime=true&charset=utf8mb4"),
		ModelList: map[string]bool{
			"deepseek-chat":     true,
			"deepseek-reasoner": true,
			"hy3-preview":       true,
		},
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
