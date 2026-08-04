// Package apicontract is generated from contracts/llama-skein.openapi.json.
//
// skip-prune keeps schemas with no path reference yet: the migration plan in
// openspec/changes/host-model-management-api/design.md adds OpenAPI schemas
// ahead of the endpoints that will use them (schemas first, routing later),
// and the default pruning behavior silently drops exactly those types.
//
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.7.0 -generate types,client,skip-prune -package apicontract -o llama_skein.gen.go ../../contracts/llama-skein.openapi.json
package apicontract
