interface CharacterReference {
  published: boolean;
  report: {
    character_image: { character_id: string; look: string; view: string };
  };
}

// A visual comparison only: it does not declare a coherent costume version or
// bind a production shot. Keep multiple adopted references available to choose.
export function characterViewSlots<T extends CharacterReference>(
  images: T[],
  characterId: string,
  look: string,
) {
  return [
    { view: "front", label: "正面" },
    { view: "side", label: "侧面" },
    { view: "back", label: "背面" },
  ].map((slot) => ({
    ...slot,
    images: images.filter((image) => {
      const meta = image.report.character_image;
      return (
        !!look &&
        image.published &&
        meta.character_id === characterId &&
        meta.look === look &&
        meta.view === slot.view
      );
    }),
  }));
}
