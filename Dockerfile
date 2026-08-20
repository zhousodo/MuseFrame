# MuseFrame API — production image.
FROM node:22-slim

WORKDIR /app

# Install prod deps first for layer caching.
COPY package.json package-lock.json ./
RUN npm ci --omit=dev

# App source (data/ and .env are mounted at runtime, never baked in).
COPY server ./server
COPY web ./web
COPY assets ./assets

ENV PORT=8787 NODE_ENV=production
EXPOSE 8787

# Node 22 has fetch/sqlite built in; run directly (PID 1 handled by compose init).
CMD ["node", "server/index.js"]
