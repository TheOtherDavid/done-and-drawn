# Personal To-Do MVP

A single-household manual task list with a character reward image generated after a task is completed. The Go service keeps task state and settings in SQLite, stores images on disk, and serves the Vue app and API from one origin.

## Requirements

- Go 1.22 or newer
- Node.js 20 or newer
- An OpenAI API key for reward image generation

## Run in development

From the repository root, start the backend:

```powershell
$env:APP_DATA_DIR = "./data"
$env:APP_ADDR = "127.0.0.1:8080"
go run ./cmd/server
```

In a second terminal, start the Vue development server:

```powershell
cd frontend
npm install
npm run dev
```

Open the URL printed by Vite. Its `/api` and `/media` requests are proxied to the Go backend.

## Build and run as one service

```powershell
cd frontend
npm install
npm run build
cd ..
$env:OPENAI_API_KEY = "your-key"
$env:APP_DATA_DIR = "./data"
$env:APP_ADDR = "0.0.0.0:8080"
go run ./cmd/server
```

The Vue build is embedded in the Go binary. The persistent data directory contains `app.sqlite` and `images/`; back up the directory together. Keep the API key in the backend environment or service configuration, never in Vue or the SQLite settings.

The default address allows other devices on the household network to connect. Restrict access to that network and do not forward the service port from the internet.

## Raspberry Pi service

Build on the Pi (or on another Linux ARM64 machine matching the Pi OS), then install the resulting binary at `/opt/image-todo/server`:

```sh
cd frontend && npm ci && npm run build
cd ..
mkdir -p build
go build -o build/server ./cmd/server
```

Create a system user and persistent data directory, install the binary and service unit, then put the backend key in `/etc/image-todo.env`:

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin image-todo
sudo install -d -o image-todo -g image-todo /opt/image-todo /var/lib/image-todo
sudo install -m 0755 build/server /opt/image-todo/server
sudo install -m 0644 deploy/image-todo.service /etc/systemd/system/image-todo.service
sudo install -m 0600 /dev/null /etc/image-todo.env
```

Add `OPENAI_API_KEY=...` and optionally `OPENAI_IMAGE_MODEL=...` to `/etc/image-todo.env`, then enable the service:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now image-todo
```

The unit binds to port 8080 on the Pi's interfaces. Only allow trusted household devices to reach it, and do not expose or forward that port to the internet.
