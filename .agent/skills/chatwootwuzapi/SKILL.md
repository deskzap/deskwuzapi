---
name: Chatwoot-Wuzapi Integration
description: Documentação técnica detalhada sobre a integração bidirecional entre Chatwoot (via API Channel) e Wuzapi (WhatsApp Gateway). Cobre payloads, endpoints e fluxos de mensagens.
---

# Chatwoot & Wuzapi Integration Master Skill

Esta skill documenta a arquitetura de integração entre o **Chatwoot** (plataforma de atendimento) e o **Wuzapi** (gateway WhatsApp), detalhando como os dados fluem entre os dois sistemas.

## 1. Arquitetura da Integração

O Wuzapi atua como um **API Channel** customizado para o Chatwoot.
- **Wuzapi -> Chatwoot**: Quando uma mensagem chega no WhatsApp, o Wuzapi processa e envia para o Chatwoot via API Channel. Detalhes em [Chatwoot API Reference](./CHATWOOT_API.md).
- **Chatwoot -> Wuzapi**: Quando um agente responde no Chatwoot, um webhook é disparado para o Wuzapi. Detalhes em [Wuzapi API Reference](./WUZAPI_API.md).

---

## 2. Chatwoot API (Consumida pelo Wuzapi)

O Wuzapi consome a API v1 do Chatwoot para sincronizar contatos, conversas e mensagens.

### 2.1. Criação de Contatos
**Endpoint**: `POST /api/v1/accounts/{account_id}/contacts`
**Payload Enviado pelo Wuzapi**:
```json
{
  "name": "Nome do Contato (ou PushName)",
  "phone_number": "+5511999999999",
  "identifier": "5511999999999@s.whatsapp.net"
}
```
**Lógica**:
- O Wuzapi busca primeiro por `identifier` (JID) usando `GET /api/v1/accounts/{account_id}/contacts/search?q={jid}`.
- Se não encontrar, cria um novo contato.

### 2.2. Criação de Conversas
**Endpoint**: `POST /api/v1/accounts/{account_id}/conversations`
**Payload Enviado pelo Wuzapi**:
```json
{
  "source_id": "5511999999999@s.whatsapp.net", // JID do WhatsApp
  "inbox_id": 123, // ID da Inbox API no Chatwoot
  "contact_id": 456, // ID do contato obtido no passo anterior
  "status": "open", // Opcional
  "additional_attributes": {
    "instance": "NomeDaInstancia"
  }
}
```
**Lógica**:
- O Wuzapi tenta encontrar uma conversa existente buscando pelos atributos.
- Se não existir ou estiver resolvida (dependendo da config), cria uma nova.

### 2.3. Envio de Mensagens (Incoming)
**Endpoint**: `POST /api/v1/accounts/{account_id}/conversations/{conversation_id}/messages`
**Payload Enviado pelo Wuzapi**:
```json
{
  "content": "Texto da mensagem recebida",
  "message_type": "incoming",
  "private": false,
  "content_type": "text", // ou "input_select" para listas, etc.
  "content_attributes": {} // Metadados adicionais, anexos, etc.
}
```
**Tratamento de Mídia**:
- Para imagens/vídeos/áudio, o Wuzapi envia como anexo multipart/form-data ou link, dependendo da configuração.

---

## 3. Wuzapi Webhook (Consumido pelo Chatwoot)

O Chatwoot envia eventos para o Wuzapi via Webhook. O endpoint no Wuzapi é `/chatwoot/webhook` ou `/chatwoot/webhook/{instance}`.

### 3.1. Payload do Webhook (Event: message_created)
Quando um agente envia uma mensagem no Chatwoot:

```json
{
  "event": "message_created",
  "id": 12345,
  "message_type": "outgoing", // CRÍTICO: Deve ser "outgoing" (ou "1" em algumas versões)
  "content": "Olá, como posso ajudar?",
  "private": false, // Mensagens privadas (notas internas) são ignoradas pelo Wuzapi
  "conversation": {
    "id": 678,
    "contact_inbox": {
      "source_id": "5511999999999@s.whatsapp.net" // Usado para identificar o destino
    },
    "meta": {
      "contact": {
        "phone_number": "+5511999999999" // Fallback para identificar destino
      }
    }
  },
  "sender": {
    "id": 1,
    "name": "Nome do Agente" // Usado se "Assinar Mensagens" estiver ativado
  },
  "attachments": [
    {
      "data_url": "https://chatwoot-bucket.s3...",
      "file_type": "image"
    }
  ]
}
```

### 3.2. Tratamento no Wuzapi
1.  **Validação**: Verifica se `event == "message_created"` e `message_type == "outgoing"` e `private == false`.
2.  **Destinatário**: Extrai o JID do `source_id` ou `phone_number`.
3.  **Assinatura**: Se configurado, adiciona `*Nome do Agente*: ` ao início da mensagem.
4.  **Envio**: Usa a biblioteca `whatsmeow` para enviar a mensagem para o WhatsApp.
5.  **Anexos**: Se houver `attachments`, faz o download da URL (`data_url`) e envia como mídia para o WhatsApp.

---

## 4. Diagnóstico e Debug

### 4.1. Ferramenta de Payload Test
Use o endpoint `/chatwoot/payload-test` para validar o que o Chatwoot está enviando.
```bash
curl -X POST https://seu-wuzapi.com/chatwoot/payload-test \
  -H "Content-Type: application/json" \
  -d '{"event":"message_created", ...}'
```

### 4.2. Problemas Comuns
- **Erro 404 no Webhook**: URL do webhook incorreta no Chatwoot. Deve ser `https://wuzapi/chatwoot/webhook`.
- **Mensagem não enviada**: Verifique se `message_type` está chegando como "outgoing". Algumas versões do Chatwoot enviam "1" ou outros valores. O Wuzapi (v1.0.10+) trata string/int.
- **Loop de mensagens**: Se o Wuzapi reenviar a mensagem para o Chatwoot como "incoming" e o Chatwoot devolver como "outgoing", cria-se um loop. O Wuzapi protege contra isso verificando IDs e tipos, mas configurações incorretas de Inbox podem causar isso.

---
