// Package mocks содержит сгенерированные заглушки интерфейсов для тестов.
package mocks

//go:generate go run go.uber.org/mock/mockgen -source=../repository/avatar_repository.go -destination=avatar_repository.go -package=mocks
//go:generate go run go.uber.org/mock/mockgen -source=../storage/storage.go -destination=storage.go -package=mocks
//go:generate go run go.uber.org/mock/mockgen -source=../services/avatar_service.go -destination=avatar_service.go -package=mocks
