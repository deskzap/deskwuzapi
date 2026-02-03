# Chatwoot API Reference (Consumida pelo Wuzapi)

Detalhes técnicos sobre os endpoints da API v1 do Chatwoot utilizados pela integração Wuzapi.

## 1. Authentication
Header: `api_access_token: {chatwoot_user_token}`
Ou `platform_api_access_token` para operações de plataforma (não usado na integração padrão de agente).

## 2. Contacts API

### Search Contact
`GET /api/v1/accounts/{account_id}/contacts/search?q={identifier}`
- Usado para evitar duplicatas. O Wuzapi busca pelo JID (ex: `5511999999999@s.whatsapp.net`).

### Create Contact
`POST /api/v1/accounts/{account_id}/contacts`
```json
{
  "inbox_id": 123,
  "name": "Nome",
  "phone_number": "+5511999999999",
  "identifier": "5511999999999@s.whatsapp.net"
}
```

## 3. Conversations API

### List Conversations (Check status)
`GET /api/v1/accounts/{account_id}/contacts/{contact_id}/conversations?status=open`
- Verifica se já existe uma conversa aberta para não criar múltiplas.

### Create Conversation
`POST /api/v1/accounts/{account_id}/conversations`
```json
{
  "source_id": "JID_DO_WHATSAPP",
  "inbox_id": 123,
  "contact_id": 456,
  "status": "open"
}
```

## 4. Messages API (Incoming to Chatwoot)

### Create Message
`POST /api/v1/accounts/{account_id}/conversations/{conversation_id}/messages`

**Texto Puro**:
```json
{
  "content": "Texto da mensagem",
  "message_type": "incoming",
  "private": false
}
```

**Com Anexos (Multipart)**:
- Campo `content`: Legenda ou vazio
- Campo `attachments[]`: Arquivo binário
- Campo `message_type`: "incoming"
- Campo `private`: "false"

## 5. Inboxes API

### Create API Inbox
`POST /api/v1/accounts/{account_id}/inboxes`
```json
{
  "name": "Nome da Caixa",
  "channel": {
    "type": "api",
    "webhook_url": "https://wuzapi-url/chatwoot/webhook"
  }
}
```
- Usado automaticamente pelo Wuzapi ao configurar uma nova integração se solicitado.
