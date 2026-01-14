# Regras e Contexto do Projeto Deskwuzapi

Este arquivo serve como guia mestre para o desenvolvimento do projeto `deskwuzapi`. Aqui documentamos a análise técnica, objetivos e padrões a serem seguidos.

## 1. Visão Geral do Projeto
O **deskwuzapi** é um fork/clone do [Wuzapi](https://github.com/asternic/wuzapi), uma API RESTful escrita em **Go** para interagir com o WhatsApp.

> [!IMPORTANT]
> **Referência de Segurança**: O repositório original é mantido em [https://github.com/asternic/wuzapi](https://github.com/asternic/wuzapi). Em caso de falhas críticas ou necessidade de *rollback* de funcionalidades, este repositório deve ser usado como "Single Source of Truth" para comparação e restauração.

O objetivo é fornecer uma interface programável para automação de mensagens e gestão de sessões do WhatsApp.

### Objetivo Atual
Personalizar e evoluir a base do Wuzapi para atender aos requisitos específicos do "Deskzap" (contexto do usuário).

## 2. Engenharia Reversa: Análise Técnica
Com base na análise do código fonte inicial (commit de clone do Wuzapi):

### Stack Tecnológica
- **Linguagem**: Go (v1.24+)
- **Core Library**: `go.mau.fi/whatsmeow` (Implementação do protocolo WhatsApp Multi-Device).
- **Servidor Web**: `github.com/gorilla/mux` para roteamento HTTP.
- **Banco de Dados**: Suporte a SQLite (padrão, `modernc.org/sqlite`) e PostgreSQL (`github.com/lib/pq`).
- **Mensageria**: Integração com RabbitMQ (`InitRabbitMQ` em `main.go`).
- **Protocolo**: HTTP (REST) e Stdio (interação via terminal/pipes).

### Arquitetura
1.  **Entry Point**: `main.go`. Configura flags, variáves de ambiente, DB, RabbitMQ e inicia o servidor (HTTP ou Stdio).
2.  **Gerenciamento de Clientes**: `clientManager` mantém as sessões ativas do WhatsApp (`whatsmeow.Client`).
3.  **Autenticação**:
    - **Token de Admin** (`WUZAPI_ADMIN_TOKEN`): Para criar/listar usuários (`/admin/users`).
    - **Token de Usuário**: Para interagir com a sessão do WhatsApp (`/chat/*`, `/session/*`).
4.  **Armazenamento**:
    - Usuários e credenciais armazenados no banco SQL (users, etc).
    - Sessões do `whatsmeow` armazenadas no banco (via `sqlstore`).
5.  **Webhooks**: O sistema envia eventos (Mensagens, Status) para URLs configuradas via webhook. Existe logica de retry e fila de erros (RabbitMQ).

### Funcionalidades Principais (Endpoints)
- **Sessão**: Conectar, Desconectar, QR Code, Status, Logout.
- **Chat**: Enviar Texto, Imagem, Áudio, Vídeo, Documento, Template, Sticker.
- **Usuário**: Info de números, verificar existência, Avatar, Contatos.
- **Admin**: Gestão de usuários do sistema API.

## 3. Diretrizes de Desenvolvimento (Regras)
Todas as novas implementações devem ser registradas aqui.

- **Configuração**: Priorizar variáveis de ambiente (`.env`).
- **Logs**: Manter padrão usando `zerolog`.
- **Database**: Manter compatibilidade com SQLite (dev) e Postgres (prod).

## 4. Histórico de Mudanças
- **[Data atual]**: Análise inicial e criação deste documento. Setup do repositório.
- **[Data atual]**: Adicionada referência segura ao repositório original Wuzapi.

