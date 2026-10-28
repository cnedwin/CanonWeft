export interface ImageRectangle {
  x: number;
  y: number;
  width: number;
  height: number;
}
export interface ImageRegion {
  source_id: string;
  width: number;
  height: number;
  areas: ImageRectangle[];
  preserve: string;
}
