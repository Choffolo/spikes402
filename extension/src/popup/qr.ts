import { encode } from "uqr";

export function invoiceQrSvg(invoice: string): string {
  const { data, size } = encode(invoice, { ecc: "M" });
  const cells: string[] = [];
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      if (data[y][x]) cells.push(`<rect x="${x}" y="${y}" width="1" height="1"/>`);
    }
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${size} ${size}" shape-rendering="crispEdges">${cells.join("")}</svg>`;
}
