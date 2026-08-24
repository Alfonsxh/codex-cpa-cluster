import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: "../../api/openapi.yaml",
  output: {
    path: process.env.CPA_OPENAPI_TS_OUTPUT ?? "../../frontend/src/api/generated"
  },
  plugins: [
    {
      name: "@hey-api/typescript",
      enums: false
    }
  ]
});
