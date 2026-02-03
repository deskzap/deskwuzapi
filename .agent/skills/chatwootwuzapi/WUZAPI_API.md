# Wuzapi API Reference for Integrations

Esta referência detalha os endpoints do Wuzapi críticos para integrações externas, especialmente Chatwoot.

## 1. Webhook (Recebimento de Eventos)
O Wuzapi envia eventos para a URL configurada no endpoint `/session/connect` ou `/webhook`.

### Payload Padrão (Message)
```json
{
  "id": "ID_DA_MENSAGEM",
  "timestamp": 1678900000,
  "data": {
    "pushName": "Nome do Usuário",
    "messageType": "conversation", // ou imageMessage, videoMessage, etc.
    "key": {
      "remoteJid": "5511999999999@s.whatsapp.net",
      "fromMe": false,
      "id": "ID_DA_MENSAGEM"
    },
    "message": {
      "conversation": "Texto da mensagem"
    }
  }
}
```
**Nota sobre Mídia**: Mensagens de mídia (`imageMessage`, etc.) contêm metadados. O binário deve ser baixado autenticando-se com a sessão se necessário, ou o Wuzapi já processa o download internamente para integrações como Chatwoot.

## 2. API de Envio (Sending API)
Endpoints usados para enviar mensagens para o WhatsApp.

### Autenticação
Header: `Token: {user_token}`

### 2.1. Enviar Texto
`POST /chat/send/text`
```json
{
  "Phone": "5511999999999",
  "Body": "Sua mensagem aqui",
  "LinkPreview": true // Opcional
}
```

### 2.2. Enviar Mídia (Base64)
Para integrações que não enviam arquivos multipart, o Wuzapi aceita Base64 embedded.

**Imagem**: `POST /chat/send/image`
```json
{
  "Phone": "5511999999999",
  "Caption": "Legenda da foto",
  "Image": "data:image/jpeg;base64,....."
}
```

**Arquivo**: `POST /chat/send/document`
```json
{
  "Phone": "5511999999999",
  "FileName": "boleto.pdf",
  "Document": "data:application/pdf;base64,....."
}
```

## 3. Integração Específica: Chatwoot Webhook Endpoint
O Wuzapi expõe um endpoint dedicado para receber eventos do Chatwoot.

`POST /chatwoot/webhook`

Este endpoint espera o payload de webhook padrão do Chatwoot (`message_created`, `message_updated`) e traduz automaticamente para envios no WhatsApp via `whatsmeow`.
Dispensando a necessidade de 'middleware' externo para tradução de payloads.

**Requisito Crítico**:
- O `message_type` deve ser identificado corretamente como `outgoing` (ou variante suportada) para que o Wuzapi saiba que deve enviar para o WhatsApp.
- O `private` deve ser `false`.
