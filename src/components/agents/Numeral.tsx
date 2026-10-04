/** A display numeral in the mono face whose "." and "," close up instead of filling a whole monospace cell. */
const Numeral = ({ text }: { text: string }) => (
  <>
    {text.split(/([.,])/).map((part, i) =>
      part === '.' || part === ',' ? (
        <span key={i} className="agents-numeral__punct">
          {part}
        </span>
      ) : (
        part
      ),
    )}
  </>
)

export default Numeral
