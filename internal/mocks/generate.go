// Package mocks содержит сгенерированные заглушки интерфейсов для тестов.
package mocks

//go:generate mockgen -source=../services/avatar_service.go -destination=services.go -package=mocks
//go:generate mockgen -source=../handlers/avatar_handler.go -destination=handlers.go -package=mocks
