import {defineConfig} from '@playwright/test';
export default defineConfig({
  testDir: './tests', workers: 1, timeout: 60000,
  use: {baseURL:'http://127.0.0.1:8766', channel:'chrome', headless:true, viewport:{width:1440,height:1000}, screenshot:'only-on-failure'},
  webServer: {command:'"..\\.venv\\Scripts\\python.exe" "..\\tests\\serve_browser.py"', url:'http://127.0.0.1:8766', reuseExistingServer:false, timeout:30000},
  reporter:'list'
});
