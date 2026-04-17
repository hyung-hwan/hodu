import { defineConfig } from "vite";
import { viteSingleFile } from "vite-plugin-singlefile";
import { resolve } from "node:path";

export default defineConfig({
	plugins: [viteSingleFile()],
	build: {
		outDir: ".vite-xterm",
		emptyOutDir: true,
		rollupOptions: {
			input: resolve(".", "xterm-plain.html")
		}
	}
});
