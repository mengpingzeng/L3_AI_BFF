// Package handler 定义小说类发布平台的统一接口。
package handler

// NovelPlatform 定义小说类发布平台（番茄/七猫/逐浪等）的自动发布行为。
// 每个平台实现自己的 Run() 和 Finalize()。
// AutoPublishManager 通过此接口路由分派执行。
type NovelPlatform interface {
	Platform() string
	Run(job *AutoPublishJob)
	Finalize(job *AutoPublishJob) error
}
