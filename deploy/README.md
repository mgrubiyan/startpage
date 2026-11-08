# Deploying to a VPS

Two compose projects on one server:

```
/opt/infra       deploy/infra      Caddy (TLS, the only public ports) + one Postgres for every app
/opt/startpage   deploy/startpage  the personal instance and the public read-only demo
```

```
internet ──443──▶ caddy ──▶ startpage        (HOME_DOMAIN, password login)
                        ├─▶ startpage-demo   (DEMO_DOMAIN, STARTPAGE_DEMO=true)
                        └─▶ 302 to DEMO_DOMAIN (APEX_DOMAIN and www.APEX_DOMAIN)
                                 │
                                 ▼
                             postgres: role+db startpage, role+db startpage_demo
```

- Infra is a separate project, so `docker compose down` in an app never stops the database
  other apps use.
- Every app has its own role and database, and `CONNECT` is revoked from `PUBLIC`. The public
  demo's role can't open the personal database.
- App containers publish no ports. Caddy sets `X-Forwarded-For` to the real client IP, so
  `STARTPAGE_TRUST_PROXY=true` is safe here and per-IP rate limits work.
- CI builds the image. The server only pulls it, and nothing compiles on the box.

## 1. DNS

Point A records for every hostname (e.g. `home.example.com`, `demo.example.com`,
`example.com` and `www.example.com`) at the server's IP before starting Caddy, or the
certificate requests fail. A wildcard `*` record plus one for `@` covers them all.

## 2. Infra

```sh
sudo mkdir -p /opt/infra /opt/startpage
# from a checkout of this repo:
scp -r deploy/infra/. root@SERVER:/opt/infra/
scp deploy/startpage/compose.yml deploy/startpage/.env.example root@SERVER:/opt/startpage/

# on the server:
cd /opt/infra
cp .env.example .env && chmod 600 .env   # fill in; passwords: openssl rand -hex 24
docker compose up -d
docker logs postgres | grep -E 'CREATE|REVOKE'   # init script ran (first start only)
```

`initdb/01-startpage.sh` runs only when the Postgres volume is empty. To add another app
later, run its SQL by hand:

```sh
docker exec -it postgres psql -U postgres
CREATE ROLE mybot LOGIN PASSWORD '...';
CREATE DATABASE mybot OWNER mybot;
REVOKE CONNECT, TEMPORARY ON DATABASE mybot FROM PUBLIC;
```

## 3. startpage

```sh
cd /opt/startpage
cp .env.example .env && chmod 600 .env   # same DB passwords as /opt/infra/.env
docker compose pull && docker compose up -d
```

The image `ghcr.io/mgrubiyan/startpage` is published by CI on every push to `main`
(tags `latest` and the commit SHA). The package is public, so the server pulls it without a
login. For a private fork, run `docker login ghcr.io` on the server with a token that has only
`read:packages`.

### Changing the password

The login password is `STARTPAGE_PASSWORD` in `/opt/startpage/.env`; there is no reset by email,
because whoever controls the server owns the instance. Set a new one and restart the personal
instance (the demo is untouched):

```sh
cd /opt/startpage
sed -i "s|^STARTPAGE_PASSWORD=.*|STARTPAGE_PASSWORD=$(openssl rand -base64 18 | tr '+/' '-_')|" .env
grep STARTPAGE_PASSWORD .env   # the new password: save it in a password manager
docker compose up -d startpage
```

At startup the app sees that the password changed and deletes every session, so all devices
(and anyone who had the old password) are logged out.

## 4. Backups

```sh
sudo crontab -e
15 3 * * * /opt/infra/backup.sh >> /var/log/startpage-backup.log 2>&1
```

The script dumps the personal database daily into `/var/backups/startpage` (mode 600) and keeps
14 days. The demo database isn't backed up, because it reseeds itself. The dumps live on the
same server, so copy them elsewhere to survive losing the server. The script's header shows how
to check that a dump restores.

## 5. Deploy from CI (optional)

The `deploy` job in `.github/workflows/ci.yml` is off until `DEPLOY_HOST` is set. After that,
each green push to `main` runs `docker compose pull && up -d` on the server, pinned to the
commit's image tag.

```sh
# on the server: a deploy user that can run docker, with a key used only by CI
sudo adduser --disabled-password deploy && sudo usermod -aG docker deploy
sudo chown -R deploy /opt/startpage
ssh-keygen -t ed25519 -f deploy_key -N '' -C startpage-ci   # on your machine
# append deploy_key.pub to /home/deploy/.ssh/authorized_keys

# on your machine:
gh variable set DEPLOY_HOST --body SERVER_IP
gh variable set DEPLOY_USER --body deploy
gh secret set DEPLOY_SSH_KEY < deploy_key
ssh-keyscan SERVER_IP | gh secret set DEPLOY_KNOWN_HOSTS
```

Membership in the `docker` group is root-equivalent on that server. The key is stored only in
GitHub's encrypted secrets, so treat it as a root key.
