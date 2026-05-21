package middleware

import (
	"net/http"
	"strings"

	"github.com/claw-studio/L3_AI_BFF/model"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret []byte

func InitJWT(secret string) {
	jwtSecret = []byte(secret)
}

type Claims struct {
	UID      string `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

var skipAuthPaths = map[string]bool{
	"/healthz":           true,
	"/api/models":        true,
	"/api/auth/login":    true,
}

var skipAuthPrefixes = []string{
	"/ws/",
}

func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if skipAuthPaths[c.Request.URL.Path] {
			c.Next()
			return
		}
		for _, prefix := range skipAuthPrefixes {
			if strings.HasPrefix(c.Request.URL.Path, prefix) {
				c.Next()
				return
			}
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			tid, _ := c.Get(model.TraceIDKey)
			err := model.ErrUnauthorized.WithTraceID(tid.(string))
			c.AbortWithStatusJSON(http.StatusUnauthorized, err)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		token, err := jwt.ParseWithClaims(tokenStr, &Claims{},
			func(t *jwt.Token) (interface{}, error) { return jwtSecret, nil },
		)
		if err != nil || !token.Valid {
			tid, _ := c.Get(model.TraceIDKey)
			err := model.ErrInvalidToken.WithTraceID(tid.(string))
			c.AbortWithStatusJSON(http.StatusUnauthorized, err)
			return
		}

		claims := token.Claims.(*Claims)
		c.Set("uid", claims.UID)
		c.Set("role", claims.Role)
		c.Set("username", claims.Username)
		c.Next()
	}
}

var ErrAdminRequired = &model.AppError{Code: 1007, Message: "需要管理员权限", HTTPStatus: 403}

func AdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if role != "admin" {
			tid, _ := c.Get(model.TraceIDKey)
			err := ErrAdminRequired
			if tid != nil {
				err = err.WithTraceID(tid.(string))
			}
			c.AbortWithStatusJSON(http.StatusForbidden, err)
			return
		}
		c.Next()
	}
}
