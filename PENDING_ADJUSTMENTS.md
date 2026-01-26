# Pendências e Ajustes Futuros

Este documento lista melhorias técnicas, débitos conhecidos e ajustes recomendados para futuras versões do Wuzapi, identificados durante a auditoria de integração.

## 1. Melhorias de Robustez (Chatwoot Integration)

### 1.1 Deduplicação de Contatos e Conversas (Prioridade: Média)
*   **Local**: `chatwoot.go` (método `findOrCreateChatwootContact`)
*   **Contexto**: O código atual tenta criar o contato diretamente ("MVP style"). Embora a API do Chatwoot geralmente trate duplicatas retornando o contato existente, a prática recomendada é buscar (Search) antes de tentar criar para evitar erros 422 ou comportamentos inesperados.
*   **Problema**: Comentário explícito no código: `// For brevity/MVP, I'll attempt create directly. Chatwoot might dedupe or I need to handle error.`
*   **Ação Recomendada**: Implementar lógica de `Search -> If Empty -> Create`.

### 1.2 Gestão de Erros de Rede no Webhook (Prioridade: Alta)
*   **Local**: `handlers_integrations.go`
*   **Contexto**: Se o Chatwoot estiver fora do ar ou o webhook falhar (timeout), a mensagem pode ser perdida na integração.
*   **Ação Recomendada**: Implementar uma fila de retentativa (Dead Letter Queue) específica para integrações, similar ao que já existe para webhooks nativos (`webhookRetryEnabled`).

## 2. Melhorias de Código (Code Debt)

### 2.1 Webhooks com Arquivos e Headers (Prioridade: Baixa)
*   **Local**: `wmiau.go` (função `sendToUserWebHookWithHmac`)
*   **TODO Identificado**: `// TODO: Update callHookFileWithHmac to support extraHeaders if needed`
*   **Contexto**: Atualmente, o envio de arquivos (webhooks com mídia) pode não estar encaminhando headers de autenticação customizados definidos na integração. Isso pode afetar integrações que exigem tokens no header para receber arquivos.

## 3. Melhorias de Segurança

### 3.1 Assinatura de Webhooks do Chatwoot (Prioridade: Média)
*   **Local**: `handlers_chatwoot.go`
*   **Contexto**: O endpoint aceita payloads com base apenas no `integration_id` na URL.
*   **Ação Recomendada**: Adicionar validação de assinatura HMAC (se o Chatwoot suportar/enviar) ou um token secreto no header para garantir que o webhook realmente veio do Chatwoot.

## 4. Documentação Pública

### 4.1 Documentar Endpoints de Integração (Prioridade: Média)
*   **Local**: `API.md`
*   **Ação**: Adicionar uma seção "Integrations API" documentando como criar uma integração via API (`POST /user/integrations`), quais os `type` aceitos (`webhook`, `chatwoot`, `n8n`) e os campos do payload `meta`.
