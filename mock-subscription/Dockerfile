# Tiny Node runtime — no browser, no AI, no secrets. Just serves 5 HTML pages.
FROM node:20-alpine

WORKDIR /app

# Dependencies first — Docker caches this layer so src/ changes rebuild fast.
COPY package.json ./
RUN npm install --omit=dev

COPY src/ ./src/

EXPOSE 3002

CMD ["node", "src/index.js"]
