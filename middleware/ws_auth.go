package middleware

import (
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

var WSUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func WSAuthRequired() func(http.ResponseWriter, *http.Request) (string, error) {
	return func(w http.ResponseWriter, r *http.Request) (string, error) {
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			tokenStr = r.Header.Get("Sec-WebSocket-Protocol")
			if strings.HasPrefix(tokenStr, "Bearer.") {
				tokenStr = strings.TrimPrefix(tokenStr, "Bearer.")
			}
		}

		if tokenStr == "" {
			return "", jwt.ErrTokenMalformed
		}

		token, err := jwt.ParseWithClaims(tokenStr, &Claims{},
			func(t *jwt.Token) (interface{}, error) { return jwtSecret, nil },
		)
		if err != nil || !token.Valid {
			return "", jwt.ErrSignatureInvalid
		}

		claims := token.Claims.(*Claims)
		return claims.UID, nil
	}
}
