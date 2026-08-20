// Loads ../.env into process.env before any adapter module is evaluated.
import { fileURLToPath } from 'node:url';
try { process.loadEnvFile(fileURLToPath(new URL('../.env', import.meta.url))); }
catch { /* no .env — remote adapter stays disabled, local engine only */ }
